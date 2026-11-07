//go:build unit

package service

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCacheReadDeductionGroupLifecycle(t *testing.T) {
	repo := &groupRepoStubForAdmin{createID: 7}
	invalidator := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{groupRepo: repo, authCacheInvalidator: invalidator}
	group, err := svc.CreateGroup(context.Background(), &CreateGroupInput{Name: "cache", Platform: PlatformOpenAI, RateMultiplier: 1, OpenAIExperimentalFeaturesEnabled: true, ForceResponseModelMatch: true, CacheReadDeductionPercent: 20.25, CacheReadAmplificationPercent: 20, CacheReadAmplificationMinInputTokens: 10000, TransferCacheWriteToInput: true})
	require.NoError(t, err)
	require.True(t, group.OpenAIExperimentalFeaturesEnabled)
	require.True(t, group.ForceResponseModelMatch)
	require.Equal(t, 20.25, group.CacheReadDeductionPercent)
	require.Equal(t, 20.0, group.CacheReadAmplificationPercent)
	require.EqualValues(t, 10000, group.CacheReadAmplificationMinInputTokens)
	require.True(t, group.TransferCacheWriteToInput)
	repo.getByID = group
	group, err = svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{})
	require.NoError(t, err)
	require.True(t, group.OpenAIExperimentalFeaturesEnabled)
	require.Equal(t, 20.25, group.CacheReadDeductionPercent)
	zero := 0.0
	group, err = svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{CacheReadDeductionPercent: &zero})
	require.NoError(t, err)
	require.Zero(t, group.CacheReadDeductionPercent)
	require.Contains(t, invalidator.groupIDs, int64(7))
	bad := 100.01
	_, err = svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{CacheReadDeductionPercent: &bad})
	require.Error(t, err)
	repo.getByID = &Group{ID: 7, Name: "cache", Platform: PlatformOpenAI, Status: StatusActive, OpenAIExperimentalFeaturesEnabled: true, ForceResponseModelMatch: true, CacheReadDeductionPercent: 20, CacheReadAmplificationPercent: 20, CacheReadAmplificationMinInputTokens: 10000, TransferCacheWriteToInput: true}
	group, err = svc.UpdateGroup(context.Background(), 7, &UpdateGroupInput{Platform: PlatformAnthropic})
	require.NoError(t, err)
	require.False(t, group.OpenAIExperimentalFeaturesEnabled)
	require.False(t, group.ForceResponseModelMatch)
	require.Zero(t, group.CacheReadDeductionPercent)
	require.Zero(t, group.CacheReadAmplificationPercent)
	require.Zero(t, group.CacheReadAmplificationMinInputTokens)
	require.False(t, group.TransferCacheWriteToInput)
}
