//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerExtraPreservesFaultFallbackOnly(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		extra := filterSchedulerExtra(map[string]any{service.OpenAIFaultFallbackOnlyExtraKey: enabled, "private_unused": "discard"})
		require.Equal(t, enabled, extra[service.OpenAIFaultFallbackOnlyExtraKey])
		require.NotContains(t, extra, "private_unused")
	}
	require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(map[string]any{service.OpenAIFaultFallbackOnlyExtraKey: true}))
}
