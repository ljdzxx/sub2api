package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotGroupForceOpenAIFastRoundtrip(t *testing.T) {
	groupID := int64(50)
	apiKey := &APIKey{
		ID: 82, UserID: 40, GroupID: &groupID, Key: "sk-fast-roundtrip", Status: StatusActive,
		User: &User{ID: 40, Status: StatusActive},
		Group: &Group{
			ID: groupID, Name: "fast-roundtrip", Platform: PlatformOpenAI, Status: StatusActive,
			Hydrated: true, ForceOpenAIFast: true, FreeOpenAIFast: true, OpenAIExperimentalFeaturesEnabled: true, ForceResponseModelMatch: true,
			CacheReadDeductionPercent: 20.25, CacheReadAmplificationPercent: 20, CacheReadAmplificationMinInputTokens: 10000,
		},
	}
	svc := &APIKeyService{}

	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))

	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.NotNil(t, materialized.Group)
	require.True(t, materialized.Group.Hydrated)
	require.True(t, materialized.Group.ForceOpenAIFast)
	require.True(t, materialized.Group.FreeOpenAIFast)
	require.True(t, materialized.Group.OpenAIExperimentalFeaturesEnabled)
	require.True(t, materialized.Group.ForceResponseModelMatch)
	require.Equal(t, 20.25, materialized.Group.CacheReadDeductionPercent)
	require.Equal(t, 20.0, materialized.Group.CacheReadAmplificationPercent)
	require.EqualValues(t, 10000, materialized.Group.CacheReadAmplificationMinInputTokens)
	require.Equal(t, apiKeyAuthSnapshotVersion, cached.Snapshot.Version)
}
