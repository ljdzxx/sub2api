//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedulerPolicyFallbackRepo struct {
	service.AccountRepository
	account *service.Account
	reads   int
}

func (r *schedulerPolicyFallbackRepo) GetByID(context.Context, int64) (*service.Account, error) {
	r.reads++
	return r.account, nil
}

func TestSchedulerCacheOpenAIUsagePolicyLegacyPayloadMisses(t *testing.T) {
	for _, field := range []string{
		"OpenAIExperimentalFeaturesEnabled",
		"OpenAINormalInputCacheRules",
	} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			cache := newSchedulerCacheUnit(t)
			account := service.Account{
				ID: 58, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
				OpenAIExperimentalFeaturesEnabled: true,
				OpenAINormalInputCacheRules: []domain.OpenAINormalInputCacheRule{
					{TransferPercent: 5, ProbabilityPercent: 99},
					{TransferPercent: 8, ProbabilityPercent: 1},
				},
			}
			bucket := service.SchedulerBucket{GroupID: 7, Platform: service.PlatformOpenAI, Mode: service.SchedulerModeSingle}
			token, err := cache.CaptureBucketWriteToken(ctx, bucket)
			require.NoError(t, err)
			require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))

			// Older instances share these Redis keys but serialize neither policy field.
			// Removing each field independently also covers incomplete projections.
			for _, key := range []string{schedulerAccountKey("58"), schedulerAccountMetaKey("58")} {
				payload, err := cache.rdb.Get(ctx, key).Bytes()
				require.NoError(t, err)
				var legacy map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(payload, &legacy))
				delete(legacy, field)
				payload, err = json.Marshal(legacy)
				require.NoError(t, err)
				require.NoError(t, cache.rdb.Set(ctx, key, payload, 0).Err())
			}

			got, err := cache.GetAccount(ctx, account.ID)
			require.NoError(t, err)
			require.Nil(t, got, "missing policy fields must trigger DB fallback, not silently disable the policy")
			snapshot, hit, err := cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.False(t, hit, "incomplete metadata must not become a reusable snapshot")
			require.Nil(t, snapshot)

			// Exercise the real hydration path: only DB state may resolve a legacy
			// payload's unknown policy, and both configured rules must transfer.
			repo := &schedulerPolicyFallbackRepo{account: &account}
			svc := service.NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
			hydrated, err := svc.GetAccount(ctx, account.ID)
			require.NoError(t, err)
			require.Equal(t, 1, repo.reads)
			require.True(t, hydrated.OpenAIExperimentalFeaturesEnabled)
			execution := service.NewOpenAIUsagePolicyExecution(nil)
			execution.BindAccount(hydrated)
			selection := execution.Snapshot()
			require.True(t, selection.Account.Enabled)
			_, effective := selection.Apply(service.OpenAIUsage{InputTokens: 1000})
			require.Contains(t, []int{50, 80}, effective.CacheCreationInputTokens)

			// Rebuilding with a current writer restores both caches and the selection.
			require.NoError(t, cache.SetAccount(ctx, &account))
			got, err = cache.GetAccount(ctx, account.ID)
			require.NoError(t, err)
			require.NotNil(t, got)
			require.True(t, got.OpenAIExperimentalFeaturesEnabled)
			require.Equal(t, account.OpenAINormalInputCacheRules, got.OpenAINormalInputCacheRules)
			snapshot, hit, err = cache.GetSnapshot(ctx, bucket)
			require.NoError(t, err)
			require.True(t, hit)
			require.Len(t, snapshot, 1)
			require.True(t, snapshot[0].OpenAIExperimentalFeaturesEnabled)
		})
	}
}

func TestSchedulerCacheOpenAIUsagePolicyExplicitlyDisabledRemainsAHit(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	account := service.Account{ID: 58, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}
	require.NoError(t, cache.SetAccount(ctx, &account))
	got, err := cache.GetAccount(ctx, account.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.False(t, got.OpenAIExperimentalFeaturesEnabled)
	require.Empty(t, got.OpenAINormalInputCacheRules)
}

func TestSchedulerCacheOpenAIUsagePolicyLegacyOtherAccountsRemainReadable(t *testing.T) {
	for _, account := range []service.Account{
		{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth},
		{ID: 2, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey},
	} {
		payload, err := json.Marshal(account)
		require.NoError(t, err)
		var legacy map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(payload, &legacy))
		delete(legacy, "OpenAIExperimentalFeaturesEnabled")
		delete(legacy, "OpenAINormalInputCacheRules")
		payload, err = json.Marshal(legacy)
		require.NoError(t, err)
		got, err := decodeCachedAccount(payload)
		require.NoError(t, err)
		require.Equal(t, account.ID, got.ID)
	}
}
