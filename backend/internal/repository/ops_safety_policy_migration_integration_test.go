//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestSafetyPolicyMigrationAndStreamingFilter(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	// Isolated synthetic logs; the temporary table shadows the real table.
	_, err = tx.ExecContext(ctx, `CREATE TEMP TABLE ops_error_logs (LIKE public.ops_error_logs INCLUDING DEFAULTS) ON COMMIT DROP`)
	require.NoError(t, err)
	fixtures := []struct{ body, want string }{
		{`{"error":{"code":"bio_policy","message":"blocked"}}`, "bio_policy"},
		{"event: response.failed\ndata: {\"response\":{\"error\":{\"code\":\"misalignment_policy_violation\"}}}\n\n", "misalignment_policy_violation"},
		{`{"choices":[{"finish_reason":"content_filter"}]}`, "content_filter"},
		{`{"error":{"code":"invalid_request","message":"bio_policy"}}`, "api_error"},
		{`{"error":{"code":"bio_policy"`, "api_error"},
		{`{"output":[{"type":"refusal","refusal":"content_filter"}]}`, "api_error"},
	}
	for i, fixture := range fixtures {
		_, err = tx.ExecContext(ctx, `INSERT INTO ops_error_logs (id, error_phase, error_type, status_code, error_body) VALUES ($1, 'upstream', 'api_error', 200, $2)`, i+1, fixture.body)
		require.NoError(t, err)
	}
	sql, err := migrations.FS.ReadFile("248_classify_safety_policy_errors.sql")
	require.NoError(t, err)
	for pass := 0; pass < 2; pass++ {
		_, err = tx.ExecContext(ctx, string(sql))
		require.NoError(t, err)
		for i, fixture := range fixtures {
			var code, body, phase string
			require.NoError(t, tx.QueryRowContext(ctx, `SELECT error_type, error_body, error_phase FROM ops_error_logs WHERE id=$1`, i+1).Scan(&code, &body, &phase))
			require.Equal(t, fixture.want, code)
			require.Equal(t, fixture.body, body)
			if code != "api_error" {
				require.Equal(t, "request", phase)
			} else {
				require.Equal(t, "upstream", phase)
			}
		}
	}
	for _, category := range []string{"safety_policy", "cyber"} {
		phases, types := service.CategoryToFilter(category)
		where, args := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{ErrorPhasesAny: phases, ErrorTypesAny: types, View: "all"})
		var total int
		require.NoError(t, tx.QueryRowContext(ctx, "SELECT count(*) FROM ops_error_logs e "+where, args...).Scan(&total))
		require.Equal(t, 3, total, "include HTTP 200 policy failures, excluding ordinary successful/recovered rows")
	}
}
