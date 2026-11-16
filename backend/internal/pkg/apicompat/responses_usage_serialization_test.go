package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesUsageMarshalIncludesCachedTokens(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		details *ResponsesInputTokensDetails
		want    int64
	}{
		{name: "missing details"},
		{name: "empty details", details: &ResponsesInputTokensDetails{}},
		{name: "cache write only", details: &ResponsesInputTokensDetails{CacheWriteTokens: 3}},
		{name: "cache hit", details: &ResponsesInputTokensDetails{CachedTokens: 8}, want: 8},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			usage := &ResponsesUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12, InputTokensDetails: testCase.details}
			for _, value := range []struct {
				name string
				body any
				path string
			}{
				{name: "usage", body: usage, path: "input_tokens_details.cached_tokens"},
				{name: "completed event", body: ResponsesStreamEvent{
					Type:     "response.completed",
					Response: &ResponsesResponse{ID: "resp_1", Status: "completed", Usage: usage},
				}, path: "response.usage.input_tokens_details.cached_tokens"},
			} {
				t.Run(value.name, func(t *testing.T) {
					payload, err := json.Marshal(value.body)
					require.NoError(t, err)
					cached := gjson.GetBytes(payload, value.path)
					require.True(t, cached.Exists(), string(payload))
					require.Equal(t, gjson.Number, cached.Type)
					require.Equal(t, testCase.want, cached.Int())
				})
			}
			require.Same(t, testCase.details, usage.InputTokensDetails)
		})
	}
}
