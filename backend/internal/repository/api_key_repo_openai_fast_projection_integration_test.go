//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGetByKeyForAuthCarriesGroupForceOpenAIFast(t *testing.T) {
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	group := mustCreateGroup(t, integrationEntClient, &service.Group{
		Name: fmt.Sprintf("fast-proj-group-%d", suffix), Platform: service.PlatformOpenAI,
		RateMultiplier: 1, ForceOpenAIFast: true, FreeOpenAIFast: true,
		OpenAIExperimentalFeaturesEnabled: true, ForceResponseModelMatch: true, CacheReadDeductionPercent: 20.25,
		CacheReadAmplificationPercent: 20, CacheReadAmplificationMinInputTokens: 10000, TransferCacheWriteToInput: true,
	})
	user := mustCreateUser(t, integrationEntClient, &service.User{
		Email: fmt.Sprintf("fast-proj-%d@example.com", suffix), Concurrency: 5,
	})
	groupID := group.ID
	keyValue := fmt.Sprintf("sk-fast-proj-%d", suffix)
	apiKeyRepo := NewAPIKeyRepository(integrationEntClient, integrationDB)
	key := &service.APIKey{UserID: user.ID, GroupID: &groupID, Key: keyValue, Name: "fast-proj", Status: service.StatusActive}
	require.NoError(t, apiKeyRepo.Create(ctx, key))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM auth_cache_invalidation_outbox WHERE cache_key = encode(sha256(convert_to($1, 'UTF8')), 'hex')", keyValue)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM api_keys WHERE id = $1", key.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", user.ID)
		require.NoError(t, err)
		_, err = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", group.ID)
		require.NoError(t, err)
	})

	got, err := apiKeyRepo.GetByKeyForAuth(ctx, keyValue)
	require.NoError(t, err)
	require.NotNil(t, got.Group)
	require.True(t, got.Group.ForceOpenAIFast)
	require.True(t, got.Group.FreeOpenAIFast)
	require.True(t, got.Group.OpenAIExperimentalFeaturesEnabled)
	require.True(t, got.Group.ForceResponseModelMatch)
	require.Equal(t, 20.25, got.Group.CacheReadDeductionPercent)
	require.Equal(t, 20.0, got.Group.CacheReadAmplificationPercent)
	require.EqualValues(t, 10000, got.Group.CacheReadAmplificationMinInputTokens)
	require.True(t, got.Group.TransferCacheWriteToInput)
}
