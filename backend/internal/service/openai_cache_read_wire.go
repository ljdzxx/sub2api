package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func newContextCacheReadTransformer(ctx context.Context) *CacheReadWireTransformer {
	if ctx == nil {
		return nil
	}
	if execution := openAIUsagePolicyExecutionFromContext(ctx); execution != nil {
		return NewOpenAIUsagePolicyWireTransformer(execution, false)
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	return NewCacheReadPolicyWireTransformer(OpenAICacheReadPolicyForKey(&APIKey{Group: group}), false)
}

// CacheReadWireTransformer operates only on a copy at the client write boundary.
// Upstream parsing, turn completion and retry accounting retain original bytes.
// One instance belongs to one HTTP request or downstream WS write goroutine.
type CacheReadWireTransformer struct {
	policy     OpenAICacheReadPolicy
	execution  *OpenAIUsagePolicyExecution
	anthropic  bool
	raw        OpenAIUsage
	haveInput  bool
	responseID string
}

func NewCacheReadWireTransformer(percent float64, anthropic bool) *CacheReadWireTransformer {
	return NewCacheReadPolicyWireTransformer(OpenAICacheReadPolicy{DeductionPercent: percent}, anthropic)
}

func NewCacheReadPolicyWireTransformer(policy OpenAICacheReadPolicy, anthropic bool) *CacheReadWireTransformer {
	return &CacheReadWireTransformer{policy: policy, anthropic: anthropic}
}

func NewOpenAIUsagePolicyWireTransformer(execution *OpenAIUsagePolicyExecution, anthropic bool) *CacheReadWireTransformer {
	return &CacheReadWireTransformer{execution: execution, anthropic: anthropic}
}

func (t *CacheReadWireTransformer) Transform(body []byte) []byte {
	if t == nil || !gjson.ValidBytes(body) {
		return body
	}
	policy := OpenAIUsagePolicySnapshot{Group: t.policy}
	if t.execution != nil {
		policy = t.execution.Snapshot()
	}
	if policy.KeepFast && !t.anthropic {
		body = rewriteOpenAIFastResponseTier(body)
	}
	if !policy.HasAdjustments() {
		return body
	}
	root := gjson.ParseBytes(body)
	event := root.Get("type").String()
	id := root.Get("response.id").String()
	if id == "" {
		id = root.Get("data.response.id").String()
	}
	if id == "" {
		id = root.Get("id").String()
	}
	if event == "message_start" || (id != "" && id != t.responseID) {
		t.raw = OpenAIUsage{}
		t.haveInput = false
		t.responseID = id
	}
	out := body
	for _, path := range []string{"usage", "response.usage", "message.usage", "data.usage", "data.response.usage"} {
		usage := root.Get(path)
		if !usage.IsObject() {
			continue
		}
		raw, _ := openAIUsageFromGJSON(usage)
		input := usage.Get("input_tokens")
		if !input.Exists() {
			input = usage.Get("prompt_tokens")
		}
		cachedPresent := false
		cachePaths := []string{"input_tokens_details.cached_tokens", "prompt_tokens_details.cached_tokens", "cache_read_input_tokens", "cache_read_tokens", "cached_tokens"}
		cacheWritePaths := []string{"input_tokens_details.cache_write_tokens", "prompt_tokens_details.cache_write_tokens", "input_tokens_details.cache_creation_tokens", "prompt_tokens_details.cache_creation_tokens", "cache_write_tokens", "cache_creation_input_tokens", "cache_write_input_tokens", "cache_creation_tokens"}
		for _, key := range cachePaths {
			cachedPresent = cachedPresent || usage.Get(key).Exists()
		}
		if !cachedPresent {
			for _, prefix := range []string{"input_tokens_details", "prompt_tokens_details"} {
				details := usage.Get(prefix + ".cached_tokens_details")
				if details.IsObject() {
					cachedPresent = true
					break
				}
			}
		}
		if t.anthropic {
			// Anthropic's ordinary input excludes both cache buckets. Streaming
			// message_delta usage is cumulative and often contains output only.
			if input.Exists() {
				t.raw.InputTokens = int(input.Int())
				t.haveInput = true
			}
			if cachedPresent {
				t.raw.CacheReadInputTokens = raw.CacheReadInputTokens
			}
			if usage.Get("cache_creation_input_tokens").Exists() {
				t.raw.CacheCreationInputTokens = raw.CacheCreationInputTokens
			}
			raw = t.raw
			raw.InputTokens += raw.CacheReadInputTokens + raw.CacheCreationInputTokens
		} else {
			if input.Exists() {
				t.raw.InputTokens = raw.InputTokens
				t.haveInput = true
			}
			if cachedPresent {
				t.raw.CacheReadInputTokens = raw.CacheReadInputTokens
			}
			for _, key := range cacheWritePaths {
				if usage.Get(key).Exists() {
					t.raw.CacheCreationInputTokens = raw.CacheCreationInputTokens
					t.raw.CacheCreation30mInputTokens = raw.CacheCreation30mInputTokens
					t.raw.CacheCreation12hInputTokens = raw.CacheCreation12hInputTokens
					break
				}
			}
			if usage.Get("input_tokens_details.image_tokens").Exists() || usage.Get("prompt_tokens_details.image_tokens").Exists() {
				t.raw.ImageInputTokens = raw.ImageInputTokens
			}
			for _, prefix := range []string{"input_tokens_details", "prompt_tokens_details"} {
				if image := usage.Get(prefix + ".cached_tokens_details.image_tokens"); image.Exists() {
					t.raw.ImageCacheReadTokens = int(image.Int())
				}
			}
			raw = t.raw
		}
		set := func(key string, value any) {
			if b, err := sjson.SetBytes(out, path+"."+key, value); err == nil {
				out = b
			}
		}
		del := func(key string) {
			if b, err := sjson.DeleteBytes(out, path+"."+key); err == nil {
				out = b
			}
		}
		// Transferring cache-write tokens does not require the total input count.
		// Clear any provisional cache-write detail before the incomplete-usage
		// branch below; otherwise streaming providers that report cache creation
		// ahead of input_tokens can leak the raw value to the downstream client.
		if policy.Group.TransferCacheWriteToInput {
			for _, key := range cacheWritePaths {
				if usage.Get(key).Exists() {
					set(key, 0)
				}
			}
			for _, prefix := range []string{"input_tokens_details", "prompt_tokens_details"} {
				for _, ttl := range []string{"ttl_30m_tokens", "ttl_12h_tokens"} {
					key := prefix + ".cache_write_tokens_details." + ttl
					if usage.Get(key).Exists() {
						set(key, 0)
					}
				}
			}
		}
		if !t.haveInput {
			// A cache-only provisional report cannot be adjusted without its total.
			// Suppress that detail until a complete cumulative usage is available.
			for _, key := range cachePaths {
				del(key)
			}
			del("input_tokens_details.cached_tokens_details")
			del("prompt_tokens_details.cached_tokens_details")
			continue
		}
		_, effective := policy.Apply(raw)
		if effective.CacheCreationInputTokens != raw.CacheCreationInputTokens {
			for _, prefix := range []string{"input_tokens_details", "prompt_tokens_details"} {
				details := prefix + ".cache_write_tokens_details"
				if usage.Get(details).IsObject() {
					set(details+".ttl_30m_tokens", effective.CacheCreation30mInputTokens)
					set(details+".ttl_12h_tokens", effective.CacheCreation12hInputTokens)
				}
			}
		}
		for _, key := range cachePaths {
			if usage.Get(key).Exists() {
				set(key, effective.CacheReadInputTokens)
			}
		}
		cacheWritePresent := false
		for _, key := range cacheWritePaths {
			if usage.Get(key).Exists() {
				cacheWritePresent = true
				set(key, effective.CacheCreationInputTokens)
			}
		}
		if !t.anthropic && input.Exists() && policy.Account.Enabled && !cacheWritePresent {
			if usage.Get("prompt_tokens").Exists() {
				set("prompt_tokens_details.cache_write_tokens", effective.CacheCreationInputTokens)
			} else {
				set("input_tokens_details.cache_write_tokens", effective.CacheCreationInputTokens)
			}
		}
		if !t.anthropic && input.Exists() && effective.InputTokens != raw.InputTokens {
			if usage.Get("prompt_tokens").Exists() {
				set("prompt_tokens", effective.InputTokens)
			} else {
				set("input_tokens", effective.InputTokens)
			}
			if totalTokens := usage.Get("total_tokens"); totalTokens.Exists() {
				adjustedTotal := totalTokens.Int() + int64(effective.InputTokens-raw.InputTokens)
				if adjustedTotal < 0 {
					adjustedTotal = 0
				}
				set("total_tokens", adjustedTotal)
			}
		}
		if !t.anthropic && input.Exists() && effective.CacheReadInputTokens > 0 {
			// Also supply the total when upstream only sent cache subcategories
			// or an earlier provisional report had to be withheld.
			if usage.Get("prompt_tokens").Exists() {
				set("prompt_tokens_details.cached_tokens", effective.CacheReadInputTokens)
			} else {
				set("input_tokens_details.cached_tokens", effective.CacheReadInputTokens)
			}
		}
		if t.anthropic {
			set("input_tokens", max(effective.InputTokens-effective.CacheReadInputTokens-effective.CacheCreationInputTokens, 0))
			set("cache_read_input_tokens", effective.CacheReadInputTokens)
			if usage.Get("cache_creation_input_tokens").Exists() || policy.Account.Enabled {
				set("cache_creation_input_tokens", effective.CacheCreationInputTokens)
			}
		}
		for _, prefix := range []string{"input_tokens_details", "prompt_tokens_details"} {
			details := prefix + ".cached_tokens_details"
			if usage.Get(details + ".image_tokens").Exists() {
				set(details+".image_tokens", effective.ImageCacheReadTokens)
			}
			if usage.Get(details + ".text_tokens").Exists() {
				set(details+".text_tokens", max(effective.CacheReadInputTokens-effective.ImageCacheReadTokens, 0))
			}
		}
	}
	return out
}

// TransformSSEEvent preserves framing and joins multi-line data fields according
// to the SSE specification. Content events and keepalives pass through unchanged.
func (t *CacheReadWireTransformer) TransformSSEEvent(event []byte) []byte {
	lines := bytes.Split(event, []byte("\n"))
	var data [][]byte
	first := -1
	for i, line := range lines {
		if bytes.HasPrefix(line, []byte("data:")) {
			if first < 0 {
				first = i
			}
			data = append(data, bytes.TrimSuffix(bytes.TrimPrefix(line[5:], []byte(" ")), []byte("\r")))
		}
	}
	if first < 0 {
		return event
	}
	payload := bytes.Join(data, []byte("\n"))
	changed := t.Transform(payload)
	if bytes.Equal(payload, changed) {
		return event
	}
	var out bytes.Buffer
	for i, line := range lines {
		if i == first {
			out.WriteString("data: ")
			out.Write(changed)
		} else if bytes.HasPrefix(line, []byte("data:")) {
			continue
		} else {
			out.Write(line)
		}
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}

// User error details must not echo a stored upstream usage snapshot. Keep the
// structured error itself; arbitrary diagnostic blobs are admin-only.
func publicUsageErrorBody(body string) string {
	if strings.TrimSpace(body) == "" {
		return body
	}
	if !gjson.Valid(body) {
		lower := strings.ToLower(body)
		for _, key := range []string{"usage", "cached_tokens", "cache_read", "deduction", "读缓存"} {
			if strings.Contains(lower, key) {
				return "Upstream request failed"
			}
		}
		return body
	}
	// Remove usage recursively, including embedded error/response objects, while
	// keeping error codes and useful diagnostics that do not contain audit data.
	return stripPrivateUsageJSON(body)
}

func stripPrivateUsageJSON(body string) string {
	var value any
	dec := json.NewDecoder(strings.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&value) != nil {
		return "Upstream request failed"
	}
	changed := false
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "usage") || strings.Contains(lower, "cache") || strings.Contains(lower, "deduction") || lower == "upstream_cost" {
					changed = true
					delete(x, key)
				} else {
					x[key] = clean(child)
				}
			}
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
		case string:
			lower := strings.ToLower(x)
			for _, key := range []string{"cached_tokens", "cache_read", "deduction", "\"usage\"", "读缓存"} {
				if strings.Contains(lower, key) {
					changed = true
					return "Upstream request failed"
				}
			}
		}
		return v
	}
	value = clean(value)
	if !changed {
		return body
	}
	result, err := json.Marshal(value)
	if err != nil {
		return "Upstream request failed"
	}
	return string(result)
}
