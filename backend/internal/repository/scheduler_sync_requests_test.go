package repository

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSchedulerMetadataPreservesSyncRequestPermission(t *testing.T) {
	for _, mode := range []service.OpenAISyncRequestMode{service.OpenAISyncRequestModeAllow, service.OpenAISyncRequestModeDeny, service.OpenAISyncRequestModeOnly} {
		account := service.Account{
			ID: 8749, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Extra: map[string]any{service.OpenAISyncRequestModeExtraKey: string(mode)},
		}
		payload, err := json.Marshal(buildSchedulerMetadataAccount(account))
		require.NoError(t, err)
		var restored service.Account
		require.NoError(t, json.Unmarshal(payload, &restored))
		require.Equal(t, mode, restored.GetOpenAISyncRequestMode())
		require.True(t, shouldEnqueueSchedulerOutboxForExtraUpdates(account.Extra))
	}
}
