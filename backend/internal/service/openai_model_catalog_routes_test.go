package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func channelCatalogFixture() (*Group, Account, *ChannelService, Channel) {
	group := &Group{ID: 10, Platform: PlatformOpenAI, ModelAllowlist: GroupModelAllowlist{
		Enabled: true, Models: []string{"codex-auto-review"},
	}}
	channel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{group.ID}, ModelMapping: map[string]map[string]string{
		PlatformOpenAI: {},
	}}
	for i := 1; i <= 6; i++ {
		public := fmt.Sprintf("public-model-%d", i)
		group.ModelAllowlist.Models = append(group.ModelAllowlist.Models, public)
		channel.ModelMapping[PlatformOpenAI][public] = "gpt-6-luna"
	}
	account := *newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	account.Status, account.Schedulable = StatusActive, true
	account.Credentials["model_mapping"] = map[string]any{
		"codex-auto-review": "codex-auto-review", "gpt-6-luna": "gpt-6-luna",
	}
	channels := &ChannelService{}
	channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{group.ID: PlatformOpenAI}))
	return group, account, channels, channel
}

func catalogRouteIDs(t *testing.T, body []byte, codex bool) []string {
	t.Helper()
	field, idField := "data", "id"
	if codex {
		field, idField = "models", "slug"
	}
	_, entries, err := modelCatalogEntries(body, field)
	require.NoError(t, err)
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, gjson.GetBytes(entry, idField).String())
	}
	return ids
}

type channelCatalogAccountRepo struct {
	codexModelsVisibilityAccountRepo
	calls int
}

func (r *channelCatalogAccountRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error) {
	r.calls++
	return r.codexModelsVisibilityAccountRepo.ListSchedulableByGroupID(ctx, groupID)
}

func TestChannelMappedLocalCatalogsAndCache(t *testing.T) {
	ctx := context.Background()
	group, account, channels, channel := channelCatalogFixture()
	repo := &channelCatalogAccountRepo{codexModelsVisibilityAccountRepo: codexModelsVisibilityAccountRepo{
		byGroup: map[int64][]Account{group.ID: {account}},
	}}
	gateway := &GatewayService{accountRepo: repo, channelService: channels,
		modelsListCache: gocache.New(time.Minute, time.Minute), modelsListCacheTTL: time.Minute}
	models := gateway.GetAvailableModelsForGroup(ctx, group, PlatformOpenAI)
	require.Equal(t, group.ModelAllowlist.Models, models)
	require.NotContains(t, models, "gpt-6-luna")
	require.Equal(t, models, gateway.GetAvailableModelsForGroup(ctx, group, PlatformOpenAI))
	require.Equal(t, 1, repo.calls, "reuse account declarations on catalog cache hit")

	s := &OpenAIGatewayService{accountRepo: repo, channelService: channels}
	manifest, configured, err := s.BuildGroupConfiguredCodexModelsManifest(ctx, group, "")
	require.NoError(t, err)
	require.True(t, configured)
	require.ElementsMatch(t, models, catalogRouteIDs(t, manifest.Body, true))
	oldETag := manifest.ETag
	// The public alias must inherit its target's capabilities.
	var manifestEntries struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	require.NoError(t, json.Unmarshal(manifest.Body, &manifestEntries))
	targetBody, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, []string{"gpt-6-luna"}, []Account{account}, group, nil, true)
	require.NoError(t, err)
	for _, entry := range manifestEntries.Models {
		if string(entry["slug"]) == `"public-model-1"` {
			require.JSONEq(t, gjson.GetBytes(targetBody, "models.0.supported_reasoning_levels").Raw, string(entry["supported_reasoning_levels"]))
		}
	}

	// A channel edit must be visible while the account cache is still warm.
	channel.ModelMapping[PlatformOpenAI]["public-model-1"] = "unsupported-target"
	channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{group.ID: PlatformOpenAI}))
	calls := repo.calls
	models = gateway.GetAvailableModelsForGroup(ctx, group, PlatformOpenAI)
	require.Equal(t, calls, repo.calls)
	require.NotContains(t, models, "public-model-1")
	require.Len(t, models, 6)
	manifest, configured, err = s.BuildGroupConfiguredCodexModelsManifest(ctx, group, oldETag)
	require.NoError(t, err)
	require.True(t, configured)
	require.False(t, manifest.NotModified)
	require.NotEqual(t, oldETag, manifest.ETag)
	require.ElementsMatch(t, models, catalogRouteIDs(t, manifest.Body, true))

	group.ModelAllowlist.Models = []string{"public-model-1"}
	models = gateway.GetAvailableModelsForGroup(ctx, group, PlatformOpenAI)
	require.NotNil(t, models, "empty mapped catalog must not fall back to defaults")
	require.Empty(t, models)
	manifest, configured, err = s.BuildGroupConfiguredCodexModelsManifest(ctx, group, "")
	require.NoError(t, err)
	require.True(t, configured)
	require.Empty(t, catalogRouteIDs(t, manifest.Body, true))

	// Account edits continue to use the existing cache invalidation mechanism.
	updatedAccount := account
	updatedAccount.Credentials = map[string]any{"model_mapping": map[string]any{"unsupported-target": "gpt-6-luna"}}
	repo.byGroup[group.ID] = []Account{updatedAccount}
	require.Empty(t, gateway.GetAvailableModelsForGroup(ctx, group, PlatformOpenAI))
	gateway.InvalidateAvailableModelsCache(&group.ID, PlatformOpenAI)
	require.Equal(t, []string{"public-model-1"}, gateway.GetAvailableModelsForGroup(ctx, group, PlatformOpenAI))
}

func TestChannelCatalogMappingOrderAndEligibility(t *testing.T) {
	for _, tc := range []struct {
		name           string
		channelMapping map[string]string
		accountMapping map[string]any
		selected       []string
		passthrough    bool
		want           []string
	}{
		{"channel then account", map[string]string{"public": "middle"}, map[string]any{"middle": "gpt-6-luna", "public": "wrong"}, []string{"public"}, false, []string{"public"}},
		{"missing account target", map[string]string{"public": "missing"}, map[string]any{"public": "gpt-6-luna"}, []string{"public"}, false, []string{}},
		{"wildcard concrete selection", map[string]string{"public-*": "middle"}, map[string]any{"middle": "gpt-6-luna"}, []string{"public-one", "public-two"}, false, []string{"public-one", "public-two"}},
		{"exact precedes wildcard", map[string]string{"public-*": "middle", "public-one": "missing"}, map[string]any{"middle": "gpt-6-luna"}, []string{"public-one", "public-two"}, false, []string{"public-two"}},
		{"passthrough ignores stale mapping", map[string]string{"public": "gpt-6-luna"}, map[string]any{"obsolete": "missing", "gpt-6-luna": "missing"}, []string{"public"}, true, []string{"public"}},
		{"group rejects public name", map[string]string{"public": "gpt-6-luna"}, map[string]any{"gpt-6-luna": "gpt-6-luna"}, []string{"denied"}, false, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group, account, channels, channel := channelCatalogFixture()
			group.ModelAllowlist.Models = tc.selected
			channel.ModelMapping[PlatformOpenAI] = tc.channelMapping
			channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{group.ID: PlatformOpenAI}))
			account.Credentials["model_mapping"] = tc.accountMapping
			account.Extra = map[string]any{"openai_passthrough": tc.passthrough}
			routes := loadOpenAIModelCatalogRoutes(context.Background(), channels, group)
			require.Equal(t, tc.want, routes.modelIDs([]Account{account}, group))
			for _, codex := range []bool{false, true} {
				body := []byte(`{"data":[{"id":"gpt-6-luna","custom":42}],"top":true}`)
				if codex {
					body = []byte(`{"models":[{"slug":"gpt-6-luna","custom":42}],"top":true}`)
				}
				projected, err := projectAccountModelsBody(body, &account, group, codex, routes)
				require.NoError(t, err)
				require.Equal(t, tc.want, catalogRouteIDs(t, projected, codex))
				require.True(t, gjson.GetBytes(projected, "top").Bool())
				if len(tc.want) > 0 {
					require.Contains(t, string(projected), `"custom":42`)
				}
			}
		})
	}
}

func TestChannelCatalogRetirementUsesFinalTarget(t *testing.T) {
	group, account, channels, channel := channelCatalogFixture()
	account.Type = AccountTypeOAuth
	account.Credentials["model_mapping"] = map[string]any{"gpt-6-luna": "gpt-6-luna", "retired": "gpt-5.5"}
	channel.ModelMapping[PlatformOpenAI] = map[string]string{"gpt-5.5": "gpt-6-luna", "current-alias": "retired"}
	group.ModelAllowlist.Models = []string{"gpt-5.5", "current-alias"}
	channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{group.ID: PlatformOpenAI}))
	routes := loadOpenAIModelCatalogRoutes(context.Background(), channels, group)
	require.Equal(t, []string{"gpt-5.5"}, routes.modelIDs([]Account{account}, group))
	for _, codex := range []bool{false, true} {
		body := []byte(`{"data":[{"id":"gpt-6-luna"},{"id":"gpt-5.5"}]}`)
		if codex {
			body = []byte(`{"models":[{"slug":"gpt-6-luna"},{"slug":"gpt-5.5"}]}`)
		}
		projected, err := projectAccountModelsBody(body, &account, group, codex, routes)
		require.NoError(t, err)
		require.Equal(t, []string{"gpt-5.5"}, catalogRouteIDs(t, projected, codex))
	}
	s := &OpenAIGatewayService{channelService: channels, accountRepo: codexModelsVisibilityAccountRepo{byGroup: map[int64][]Account{group.ID: {account}}}}
	manifest, configured, err := s.BuildGroupConfiguredCodexModelsManifest(context.Background(), group, "")
	require.NoError(t, err)
	require.True(t, configured)
	require.Equal(t, []string{"gpt-5.5"}, catalogRouteIDs(t, manifest.Body, true))
	require.NoError(t, s.MergeGroupConfiguredCodexModels(context.Background(), group, manifest, ""))
	require.Equal(t, []string{"gpt-5.5"}, catalogRouteIDs(t, manifest.Body, true))
}

func TestChannelMappedPinnedCatalogsUseRawCache(t *testing.T) {
	for _, codex := range []bool{false, true} {
		t.Run(fmt.Sprint("codex=", codex), func(t *testing.T) {
			ctx := context.Background()
			group, account, channels, channel := channelCatalogFixture()
			account.Credentials["model_mapping"].(map[string]any)["unavailable"] = "unavailable"
			group.CodexModelsManifestConfig = GroupCodexModelsManifestConfig{Enabled: true, AccountIDs: []int64{account.ID}}
			var calls atomic.Int32
			s := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
				calls.Add(1)
				if codex {
					return ordinaryModelsUpstreamResponse(`{"models":[{"slug":"codex-auto-review"},{"slug":"gpt-6-luna","custom":42}]}`), nil
				}
				return ordinaryModelsUpstreamResponse(`{"data":[{"id":"codex-auto-review"},{"id":"gpt-6-luna","custom":42}]}`), nil
			}})
			s.channelService = channels
			s.accountRepo = splitCodexModelsAccountRepo{all: map[int64][]Account{group.ID: {account}}}
			fetch := func(etag string) *OpenAIModelsResponse {
				t.Helper()
				if codex {
					response, _, err := s.FetchPinnedCodexModelsManifest(ctx, group, CodexCanonicalClientVersion())
					require.NoError(t, err)
					require.NoError(t, s.MergeGroupConfiguredCodexModels(ctx, group, response, etag))
					return response
				}
				response, _, err := s.FetchPinnedOpenAIModelsList(ctx, group, 3, etag)
				require.NoError(t, err)
				return response
			}
			first := fetch("")
			require.Equal(t, group.ModelAllowlist.Models, catalogRouteIDs(t, first.Body, codex))
			require.Contains(t, string(first.Body), `"custom":42`)
			require.True(t, fetch(first.ETag).NotModified)
			channel.ModelMapping[PlatformOpenAI]["public-model-1"] = "unavailable"
			channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{group.ID: PlatformOpenAI}))
			updated := fetch(first.ETag)
			require.False(t, updated.NotModified)
			require.NotEqual(t, first.ETag, updated.ETag)
			require.NotContains(t, catalogRouteIDs(t, updated.Body, codex), "public-model-1")
			require.Len(t, catalogRouteIDs(t, updated.Body, codex), 6)
			require.EqualValues(t, 1, calls.Load(), "channel projection must not invalidate the upstream cache")
			var raw *OpenAIModelsResponse
			var err error
			if codex {
				raw, err = s.FetchCodexModelsManifest(ctx, &account, CodexCanonicalClientVersion(), "")
			} else {
				raw, err = s.FetchOpenAIModelsList(ctx, &account)
			}
			require.NoError(t, err)
			require.False(t, strings.Contains(string(raw.Body), "public-model"))
			require.Contains(t, catalogRouteIDs(t, raw.Body, codex), "gpt-6-luna")
		})
	}
}

func TestChannelCatalogIsolationAndUnmappedDefaults(t *testing.T) {
	ctx := context.Background()
	group, account, channels, channel := channelCatalogFixture()
	for _, tc := range []struct {
		name     string
		groupID  int64
		platform string
		status   string
	}{
		{"different group", 11, PlatformOpenAI, StatusActive},
		{"different platform", 10, PlatformAnthropic, StatusActive},
		{"disabled channel", 10, PlatformOpenAI, StatusDisabled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copyChannel := channel
			copyChannel.Status = tc.status
			channels.cache.Store(populateChannelCache([]Channel{copyChannel}, map[int64]string{10: PlatformOpenAI}))
			require.Nil(t, loadOpenAIModelCatalogRoutes(ctx, channels, &Group{ID: tc.groupID, Platform: tc.platform}))
		})
	}
	channels.cache.Store(populateChannelCache([]Channel{channel}, map[int64]string{group.ID: PlatformOpenAI}))
	group.ModelAllowlist.Enabled = false
	account.Extra = map[string]any{"openai_passthrough": true}
	account.Credentials["model_mapping"] = map[string]any{"obsolete": "missing"}
	routes := loadOpenAIModelCatalogRoutes(ctx, channels, group)
	models := routes.modelIDs([]Account{account}, group)
	require.Contains(t, models, "gpt-6-luna")
	require.Contains(t, models, "public-model-1")
	require.NotContains(t, models, "obsolete")
	require.NotContains(t, models, "missing")
}
