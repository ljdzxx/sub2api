package admin

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupRequestsDecodeForceOpenAIFast(t *testing.T) {
	var createReq CreateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"name":"fast","force_openai_fast":true,"free_openai_fast":true,"openai_experimental_features_enabled":true,"force_response_model_match":true,"cache_read_amplification_percent":20,"cache_read_amplification_min_input_tokens":10000,"transfer_cache_write_to_input":true}`), &createReq))
	require.True(t, createReq.ForceOpenAIFast)
	require.True(t, createReq.FreeOpenAIFast)
	require.True(t, createReq.OpenAIExperimentalFeaturesEnabled)
	require.True(t, createReq.ForceResponseModelMatch)
	require.Equal(t, 20.0, createReq.CacheReadAmplificationPercent)
	require.EqualValues(t, 10000, createReq.CacheReadAmplificationMinInputTokens)
	require.True(t, createReq.TransferCacheWriteToInput)

	var updateReq UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{"force_openai_fast":false,"free_openai_fast":false,"openai_experimental_features_enabled":false,"force_response_model_match":false,"cache_read_amplification_percent":0,"cache_read_amplification_min_input_tokens":0,"transfer_cache_write_to_input":false}`), &updateReq))
	require.NotNil(t, updateReq.ForceOpenAIFast)
	require.False(t, *updateReq.ForceOpenAIFast)
	require.NotNil(t, updateReq.FreeOpenAIFast)
	require.False(t, *updateReq.FreeOpenAIFast)
	require.NotNil(t, updateReq.OpenAIExperimentalFeaturesEnabled)
	require.False(t, *updateReq.OpenAIExperimentalFeaturesEnabled)
	require.NotNil(t, updateReq.ForceResponseModelMatch)
	require.False(t, *updateReq.ForceResponseModelMatch)
	require.NotNil(t, updateReq.CacheReadAmplificationPercent)
	require.Zero(t, *updateReq.CacheReadAmplificationPercent)
	require.NotNil(t, updateReq.CacheReadAmplificationMinInputTokens)
	require.Zero(t, *updateReq.CacheReadAmplificationMinInputTokens)
	require.NotNil(t, updateReq.TransferCacheWriteToInput)
	require.False(t, *updateReq.TransferCacheWriteToInput)

	var omitted UpdateGroupRequest
	require.NoError(t, json.Unmarshal([]byte(`{}`), &omitted))
	require.Nil(t, omitted.ForceOpenAIFast)
	require.Nil(t, omitted.FreeOpenAIFast)
	require.Nil(t, omitted.OpenAIExperimentalFeaturesEnabled)
	require.Nil(t, omitted.ForceResponseModelMatch)
	require.Nil(t, omitted.CacheReadAmplificationPercent)
	require.Nil(t, omitted.CacheReadAmplificationMinInputTokens)
	require.Nil(t, omitted.TransferCacheWriteToInput)
}
