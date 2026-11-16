package migrations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPaymentInstanceRechargePolicyMigration(t *testing.T) {
	content, err := FS.ReadFile("254_payment_instance_recharge_policy.sql")
	require.NoError(t, err)
	sql := string(content)
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS recharge_policy JSONB")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS pricing_snapshot JSONB")
	require.Contains(t, sql, "WHERE recharge_policy IS NULL")
	require.Contains(t, sql, "RECHARGE_BONUS_TIERS")
	require.Contains(t, sql, "RECHARGE_BONUS_MODE")
	require.Contains(t, sql, "RECHARGE_BONUS_NOTICE")
	require.Contains(t, sql, "SET DEFAULT '{}'::jsonb")
}
