package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCacheReadWireCompletedIncludesCachedTokens(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		usage string
		want  int
	}{
		{name: "missing details", usage: `{"input_tokens":10,"output_tokens":2}`},
		{name: "empty details", usage: `{"input_tokens":10,"input_tokens_details":{}}`},
		{name: "null details", usage: `{"input_tokens":10,"input_tokens_details":null}`},
		{name: "null cached tokens", usage: `{"input_tokens":10,"input_tokens_details":{"cached_tokens":null}}`},
		{name: "zero input", usage: `{"input_tokens":0,"output_tokens":2}`},
		{name: "output only", usage: `{"output_tokens":2}`},
		{name: "cache write only", usage: `{"input_tokens":10,"input_tokens_details":{"cache_write_tokens":3}}`},
		{name: "explicit zero", usage: `{"input_tokens":10,"input_tokens_details":{"cached_tokens":0}}`},
		{name: "cache hit", usage: `{"input_tokens":10,"input_tokens_details":{"cached_tokens":8}}`, want: 8},
		{name: "cache alias", usage: `{"input_tokens":10,"cache_read_input_tokens":8}`, want: 8},
		{name: "cache subcategories", usage: `{"input_tokens":10,"input_tokens_details":{"cached_tokens_details":{"text_tokens":6,"image_tokens":2}}}`, want: 8},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := []byte(`{"type":"response.completed","response":{"id":"resp_1","usage":` + testCase.usage + `}}`)
			for _, percent := range []float64{0, 20, 100} {
				transform := NewCacheReadWireTransformer(percent, false)
				payload := transform.Transform(body)
				cached := gjson.GetBytes(payload, "response.usage.input_tokens_details.cached_tokens")
				require.True(t, cached.Exists(), "percent=%v payload=%s", percent, payload)
				require.Equal(t, gjson.Number, cached.Type)
				want := testCase.want
				if percent > 0 && want > 0 {
					want = max(want-int(percent/10), 0)
				}
				require.EqualValues(t, want, cached.Int(), "percent=%v", percent)
				require.JSONEq(t, `{"type":"response.completed","response":{"id":"resp_1","usage":`+testCase.usage+`}}`, string(body))
			}
		})
	}
}

func TestCacheReadWireCompletedPreservesOtherPayloads(t *testing.T) {
	for _, body := range []string{
		`{"type":"response.created","response":{"usage":{"input_tokens":10}}}`,
		`{"type":"response.completed","response":{"id":"resp_1"}}`,
		`{"type":"response.completed","response":{"usage":null}}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":10}}`,
		`{"usage":{"prompt_tokens":10}}`,
		`[DONE]`,
	} {
		require.Equal(t, body, string(NewCacheReadWireTransformer(0, false).Transform([]byte(body))))
	}
	body := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":10}}}`)
	require.Equal(t, body, NewCacheReadWireTransformer(0, true).Transform(body))
}
