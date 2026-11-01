package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupRequestsDecodeForceOpenAIFast(t *testing.T) {
	var createReq CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"fast","force_openai_fast":true,"free_openai_fast":true,"openai_experimental_features_enabled":true,"cache_read_amplification_percent":20,"cache_read_amplification_min_input_tokens":10000}`), &createReq))
	require.True(t, createReq.ForceOpenAIFast)
	require.True(t, createReq.FreeOpenAIFast)
	require.True(t, createReq.OpenAIExperimentalFeaturesEnabled)
	require.Equal(t, 20.0, createReq.CacheReadAmplificationPercent)
	require.EqualValues(t, 10000, createReq.CacheReadAmplificationMinInputTokens)

	var updateReq UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"force_openai_fast":false,"free_openai_fast":false,"openai_experimental_features_enabled":false,"cache_read_amplification_percent":0,"cache_read_amplification_min_input_tokens":0}`), &updateReq))
	require.NotNil(t, updateReq.ForceOpenAIFast)
	require.False(t, *updateReq.ForceOpenAIFast)
	require.NotNil(t, updateReq.FreeOpenAIFast)
	require.False(t, *updateReq.FreeOpenAIFast)
	require.NotNil(t, updateReq.OpenAIExperimentalFeaturesEnabled)
	require.False(t, *updateReq.OpenAIExperimentalFeaturesEnabled)
	require.NotNil(t, updateReq.CacheReadAmplificationPercent)
	require.Zero(t, *updateReq.CacheReadAmplificationPercent)
	require.NotNil(t, updateReq.CacheReadAmplificationMinInputTokens)
	require.Zero(t, *updateReq.CacheReadAmplificationMinInputTokens)

	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.ForceOpenAIFast)
	require.Nil(t, omitted.FreeOpenAIFast)
	require.Nil(t, omitted.OpenAIExperimentalFeaturesEnabled)
	require.Nil(t, omitted.CacheReadAmplificationPercent)
	require.Nil(t, omitted.CacheReadAmplificationMinInputTokens)
}
