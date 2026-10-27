//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerExtraPreservesFaultFallbackOnly(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		extra := filterSchedulerExtra(map[string]any{
			service.OpenAIFaultFallbackOnlyExtraKey: enabled, service.OpenAIFaultFallbackStatusCodesExtraKey: []int{401, 503},
			service.OpenAIFaultFallbackKeywordsExtraKey: []string{"insufficient balance"}, service.OpenAIFaultFallbackMatchModeExtraKey: "all",
			"private_unused": "discard",
		})
		require.Equal(t, enabled, extra[service.OpenAIFaultFallbackOnlyExtraKey])
		require.Equal(t, []int{401, 503}, extra[service.OpenAIFaultFallbackStatusCodesExtraKey])
		require.Equal(t, []string{"insufficient balance"}, extra[service.OpenAIFaultFallbackKeywordsExtraKey])
		require.Equal(t, "all", extra[service.OpenAIFaultFallbackMatchModeExtraKey])
		require.NotContains(t, extra, "private_unused")
	}
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{service.OpenAIFaultFallbackOnlyExtraKey: true}))
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{service.OpenAIFaultFallbackStatusCodesExtraKey: []int{401}}))
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{service.OpenAIFaultFallbackKeywordsExtraKey: []string{}}))
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{service.OpenAIFaultFallbackMatchModeExtraKey: "any"}))
}
