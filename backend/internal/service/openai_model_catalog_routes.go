package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

// A catalog uses one channel snapshot, after reading the account/upstream
// cache. Channel edits therefore take effect without expiring those caches.
type openAIModelCatalogRoutes struct {
	lookup  *channelLookup
	groupID int64
}

func loadOpenAIModelCatalogRoutes(ctx context.Context, channels *ChannelService, group *Group) *openAIModelCatalogRoutes {
	if channels == nil || group == nil || group.Platform != PlatformOpenAI {
		return nil
	}
	lk, err := channels.lookupGroupChannel(ctx, group.ID)
	if err != nil {
		slog.Warn("failed to load channel mapping for model catalog", "group_id", group.ID, "error", err)
		return nil
	}
	if lk == nil || lk.platform != PlatformOpenAI || len(lk.channel.ModelMapping[PlatformOpenAI]) == 0 {
		return nil
	}
	return &openAIModelCatalogRoutes{lookup: lk, groupID: group.ID}
}

func (r *openAIModelCatalogRoutes) target(model string) string {
	if r == nil {
		return model
	}
	return resolveMapping(r.lookup, r.groupID, model).MappedModel
}

func (r *openAIModelCatalogRoutes) candidates(source []string, group *Group) []string {
	models := append([]string(nil), source...)
	aliases := make([]string, 0)
	if r != nil {
		for alias := range r.lookup.channel.ModelMapping[PlatformOpenAI] {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	models = append(models, aliases...)
	if group.ModelAllowlistEnabled() {
		models = append(models, group.ModelAllowlist.Models...)
	}
	seen := make(map[string]struct{}, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || strings.Contains(model, "*") {
			continue
		}
		// Admission is always checked against the client name, never the target.
		if group.ModelAllowlistEnabled() && !group.ModelAllowlist.Allows(model) {
			continue
		}
		if _, exists := seen[model]; !exists {
			seen[model] = struct{}{}
			result = append(result, model)
		}
	}
	return result
}

func (r *openAIModelCatalogRoutes) modelIDs(accounts []Account, group *Group) []string {
	// Account mapping memoization mutates the Account value. Never mutate the
	// shared slice held by the listing cache while concurrent readers use it.
	accounts = slices.Clone(accounts)
	// Do not filter retirement before resolving the channel replacement.
	mappedAccounts := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if !account.IsOpenAIPassthroughEnabled() {
			mappedAccounts = append(mappedAccounts, account)
		}
	}
	source := supplementUnmappedOpenAIModels(accounts, openAIConfiguredCodexModelIDs(mappedAccounts))
	if len(source) == 0 {
		for i := range accounts {
			account := &accounts[i]
			if account.IsOpenAI() && (account.IsOpenAIPassthroughEnabled() || len(account.GetModelMapping()) == 0) {
				source = openai.DefaultModelIDs()
				break
			}
		}
	}
	models := make([]string, 0)
	for _, model := range r.candidates(source, group) {
		target := r.target(model)
		for i := range accounts {
			if accounts[i].IsOpenAI() && accounts[i].IsModelSupported(target) {
				models = append(models, model)
				break
			}
		}
	}
	if group.ModelAllowlistEnabled() {
		// nil means default discovery to callers; an empty catalog is authoritative.
		return append([]string{}, group.ModelAllowlist.FilterForListing(models)...)
	}
	sort.Strings(models)
	return models
}

// This cache holds account declarations, not their channel-projected result.
// It shares the existing group/platform key and account invalidation policy.
type openAIModelCatalogAccounts struct{ accounts []Account }

func (s *GatewayService) availableChannelMappedModels(ctx context.Context, group *Group, routes *openAIModelCatalogRoutes) []string {
	key := modelsListCacheKey(&group.ID, PlatformOpenAI)
	if s.modelsListCache != nil {
		if cached, found := s.modelsListCache.Get(key); found {
			if catalog, ok := cached.(openAIModelCatalogAccounts); ok {
				modelsListCacheHitTotal.Add(1)
				return routes.modelIDs(catalog.accounts, group)
			}
		}
	}
	modelsListCacheMissTotal.Add(1)
	accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, group.ID)
	if err != nil {
		return []string{}
	}
	if s.modelsListCache != nil {
		s.modelsListCache.Set(key, openAIModelCatalogAccounts{accounts: accounts}, s.modelsListCacheTTL)
		modelsListCacheStoreTotal.Add(1)
	}
	return routes.modelIDs(accounts, group)
}

// GetAvailableModelsForGroup includes concrete selections that expand wildcard
// channel mappings, while preserving the existing policy on other platforms.
func (s *GatewayService) GetAvailableModelsForGroup(ctx context.Context, group *Group, platform string) []string {
	var groupID *int64
	if group != nil {
		groupID = &group.ID
	}
	if platform == PlatformOpenAI {
		if routes := loadOpenAIModelCatalogRoutes(ctx, s.channelService, group); routes != nil {
			return s.availableChannelMappedModels(ctx, group, routes)
		}
	}
	return s.getAccountAvailableModels(ctx, groupID, platform)
}

func (s *GatewayService) GetAvailableModels(ctx context.Context, groupID *int64, platform string) []string {
	var group *Group
	if groupID != nil {
		group = &Group{ID: *groupID, Platform: platform}
	}
	return s.GetAvailableModelsForGroup(ctx, group, platform)
}

// Build capabilities from channel targets, then expose the admitted public IDs.
// Account mapping and capability intersection remain in the existing builder.
func (r *openAIModelCatalogRoutes) buildCodexManifest(models []string, accounts []Account, group *Group) ([]byte, error) {
	targets := make([]string, 0, len(models))
	for _, model := range models {
		targets = append(targets, r.target(model))
	}
	body, err := buildCodexModelsManifestForAccounts(PlatformOpenAI, targets, accounts, group, nil, true)
	if err != nil {
		return nil, err
	}
	envelope, entries, err := modelCatalogEntries(body, "models")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]json.RawMessage, len(entries))
	for _, raw := range entries {
		var entry struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, err
		}
		byID[entry.Slug] = raw
	}
	projected := make([]json.RawMessage, 0, len(models))
	for _, model := range models {
		target := r.target(model)
		if raw, ok := byID[target]; ok {
			entry, err := renameCatalogModel(raw, "slug", model, target)
			if err != nil {
				return nil, err
			}
			projected = append(projected, entry)
		}
	}
	envelope["models"], err = json.Marshal(projected)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

func renameCatalogModel(raw json.RawMessage, idField, model, target string) (json.RawMessage, error) {
	if model == target {
		return raw, nil
	}
	var entry map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, err
	}
	entry[idField], _ = json.Marshal(model)
	entry["display_name"], _ = json.Marshal(model)
	return json.Marshal(entry)
}
