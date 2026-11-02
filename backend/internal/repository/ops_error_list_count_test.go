package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 总数必须是带 LIMIT 的有界计数：精确 COUNT(*) 会扫完整个时间窗。
func TestListErrorLogs_CountIsCapped(t *testing.T) {
	for _, tc := range []struct {
		name   string
		count  int
		capped bool
	}{
		{"under cap", 37, false},
		{"at cap", opsErrorLogCountCap, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			repo := &opsRepository{db: db}

			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM \(SELECT 1 FROM ops_error_logs e WHERE .+ LIMIT 10000\) capped`).
				WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(tc.count))
			mock.ExpectQuery(`FROM ops_error_logs e`).WillReturnRows(sqlmock.NewRows([]string{"id"}))

			out, err := repo.ListErrorLogs(context.Background(), &service.OpsErrorLogFilter{Page: 1, PageSize: 20})
			require.NoError(t, err)
			require.Equal(t, tc.count, out.Total)
			require.Equal(t, tc.capped, out.TotalCapped)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// request_id / client_request_id 精确匹配必须写裸列，才能命中 btree 索引。
func TestBuildOpsErrorLogsWhere_CorrelationKeysUseIndexableColumns(t *testing.T) {
	where, args := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{RequestID: " req-1 ", ClientRequestID: "creq-1"})
	require.Contains(t, where, "e.request_id = $")
	require.Contains(t, where, "e.client_request_id = $")
	require.False(t, strings.Contains(where, "COALESCE(e.request_id"), where)
	require.False(t, strings.Contains(where, "COALESCE(e.client_request_id"), where)
	require.Contains(t, args, "req-1")
	require.Contains(t, args, "creq-1")
}
