package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupMapperExposesForceOpenAIFastOnlyToAdmins(t *testing.T) {
	group := &service.Group{
		ID: 7, Name: "fast", Platform: service.PlatformOpenAI, Status: service.StatusActive, ForceOpenAIFast: true, FreeOpenAIFast: true,
		OpenAIExperimentalFeaturesEnabled: true, CacheReadDeductionPercent: 20.25,
		CacheReadAmplificationPercent: 20, CacheReadAmplificationMinInputTokens: 10000,
	}

	userJSON, err := json.Marshal(GroupFromService(group))
	require.NoError(t, err)
	require.NotContains(t, string(userJSON), "force_openai_fast")
	require.NotContains(t, string(userJSON), "free_openai_fast")
	require.NotContains(t, string(userJSON), "openai_experimental_features_enabled")
	require.NotContains(t, string(userJSON), "cache_read_deduction_percent")
	require.NotContains(t, string(userJSON), "cache_read_amplification_percent")

	adminJSON, err := json.Marshal(GroupFromServiceAdmin(group))
	require.NoError(t, err)
	require.Contains(t, string(adminJSON), `"force_openai_fast":true`)
	require.Contains(t, string(adminJSON), `"free_openai_fast":true`)
	require.Contains(t, string(adminJSON), `"openai_experimental_features_enabled":true`)
	require.Contains(t, string(adminJSON), `"cache_read_deduction_percent":20.25`)
	require.Contains(t, string(adminJSON), `"cache_read_amplification_percent":20`)
	require.Contains(t, string(adminJSON), `"cache_read_amplification_min_input_tokens":10000`)
}
