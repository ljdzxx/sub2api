//go:build unit

package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type cacheReadAuditScanner struct{ value sql.NullString }

func (s cacheReadAuditScanner) Scan(dest ...any) error {
	// All other destination variables intentionally retain their zero values.
	*dest[len(dest)-1].(*sql.NullString) = s.value
	return nil
}

func TestCacheReadAdjustmentPersistence(t *testing.T) {
	audit := &service.CacheReadAdjustment{Version: 1, Percent: 20, Raw: service.OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 9630}, Effective: service.OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 7630}, UpstreamCost: 0.125}
	prepared := prepareUsageLogInsert(&service.UsageLog{CacheReadAdjustment: audit})
	payload, ok := prepared.args[len(prepared.args)-1].(string)
	require.True(t, ok)
	require.Equal(t, "jsonb", usageLogInsertArgTypes[len(usageLogInsertArgTypes)-1])
	require.True(t, strings.HasSuffix(usageLogSelectColumns, "cache_read_adjustment"))
	log, err := scanUsageLog(cacheReadAuditScanner{value: sql.NullString{String: payload, Valid: true}})
	require.NoError(t, err)
	require.Equal(t, audit, log.CacheReadAdjustment)
	historical, err := scanUsageLog(cacheReadAuditScanner{})
	require.NoError(t, err)
	require.Nil(t, historical.CacheReadAdjustment)
}

func TestCacheReadAuditStatsAreOptIn(t *testing.T) {
	for _, admin := range []bool{false, true} {
		db, mock := newSQLMock(t)
		repo := &usageLogRepository{sql: db}
		columns := []string{"ig", "ug", "inbound", "upstream", "count", "input", "output", "write", "read", "cost", "actual", "account", "duration"}
		rows := sqlmock.NewRows(columns)
		query := "(?s)WITH scoped AS.*GROUP BY GROUPING SETS"
		if admin {
			columns = append(columns, "raw_read", "raw_cost")
			rows = sqlmock.NewRows(columns).AddRow(1, 1, nil, nil, 1, 2370, 20, 0, 7630, 1.5, 1.5, 0.5, 100, 9630, 0.5)
			query = "(?s)cache_read_adjustment->'raw'->>'cache_read_input_tokens'"
		} else {
			rows.AddRow(1, 1, nil, nil, 1, 2370, 20, 0, 7630, 1.5, 1.5, 0.5, 100)
		}
		mock.ExpectQuery(query).WillReturnRows(rows)
		stats, err := repo.GetStatsWithFilters(context.Background(), usagestats.UsageLogFilters{IncludeCacheReadAudit: admin})
		require.NoError(t, err)
		require.EqualValues(t, 7630, stats.TotalCacheReadTokens)
		if admin {
			require.EqualValues(t, 9630, stats.CacheReadAudit.UpstreamCacheReadTokens)
		} else {
			require.Nil(t, stats.CacheReadAudit)
			payload, err := json.Marshal(stats)
			require.NoError(t, err)
			require.NotContains(t, string(payload), "cache_read_audit")
		}
		require.NoError(t, mock.ExpectationsWereMet())
	}
}
