package dto

import (
	"encoding/json"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCacheReadAdjustmentAdminOnly(t *testing.T) {
	log := &service.UsageLog{CacheReadTokens: 7630, CacheReadAdjustment: &service.CacheReadAdjustment{Version: 1, Percent: 20, Raw: service.OpenAIUsage{CacheReadInputTokens: 9630}}}
	public, err := json.Marshal(UsageLogFromService(log))
	require.NoError(t, err)
	require.NotContains(t, string(public), "cache_read_adjustment")
	require.NotContains(t, string(public), "9630")
	admin, err := json.Marshal(UsageLogFromServiceAdmin(log))
	require.NoError(t, err)
	require.Contains(t, string(admin), "9630")
	require.Contains(t, string(admin), "7630")
	g := &service.Group{Platform: service.PlatformOpenAI, CacheReadDeductionPercent: 20}
	public, err = json.Marshal(GroupFromService(g))
	require.NoError(t, err)
	require.NotContains(t, string(public), "cache_read_deduction_percent")
}
