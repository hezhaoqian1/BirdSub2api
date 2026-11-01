package service

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestCleanupOpsQueueRejectsDeletesOnlyQueueFullRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -5)

	errLogDelete := regexp.QuoteMeta(`SELECT id FROM ops_error_logs
  WHERE created_at < $1 AND (` + opsQueueRejectErrorLogFilter + `)`)
	mock.ExpectExec(errLogDelete).WithArgs(cutoff, opsCleanupBatchSize).WillReturnResult(sqlmock.NewResult(0, 5000))
	mock.ExpectExec(errLogDelete).WithArgs(cutoff, opsCleanupBatchSize).WillReturnResult(sqlmock.NewResult(0, 1200))
	mock.ExpectExec(errLogDelete).WithArgs(cutoff, opsCleanupBatchSize).WillReturnResult(sqlmock.NewResult(0, 0))
	aggDelete := regexp.QuoteMeta(`SELECT id FROM ops_ingress_reject_aggregates
  WHERE bucket_start < $1 AND (reject_reason = 'gateway_queue_full')`)
	mock.ExpectExec(aggDelete).WithArgs(cutoff, opsCleanupBatchSize).WillReturnResult(sqlmock.NewResult(0, 40))
	mock.ExpectExec(aggDelete).WithArgs(cutoff, opsCleanupBatchSize).WillReturnResult(sqlmock.NewResult(0, 0))

	logs, aggs, err := cleanupOpsQueueRejects(context.Background(), db, now, 5, 30)
	require.NoError(t, err)
	require.Equal(t, int64(6200), logs)
	require.Equal(t, int64(40), aggs)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupOpsQueueRejectsSkipsWhenRegularRetentionCovers(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	now := time.Now()
	for _, tc := range []struct{ queueDays, errorDays int }{
		{5, 0},  // 错误日志每次整表清空
		{5, 5},  // 一样长
		{10, 5}, // 错误日志更短
		{0, 30}, // 关闭
	} {
		logs, aggs, err := cleanupOpsQueueRejects(context.Background(), db, now, tc.queueDays, tc.errorDays)
		require.NoError(t, err)
		require.Zero(t, logs+aggs)
	}
	require.NoError(t, mock.ExpectationsWereMet()) // 没有任何 SQL
}

func TestOpsQueueRejectRetentionDefaultsAndValidation(t *testing.T) {
	cfg := defaultOpsAdvancedSettings()
	require.Equal(t, 5, cfg.DataRetention.QueueRejectRetentionDays)
	require.NoError(t, validateOpsAdvancedSettings(cfg))
	cfg.DataRetention.QueueRejectRetentionDays = -1
	require.Error(t, validateOpsAdvancedSettings(cfg))
	cfg.DataRetention.QueueRejectRetentionDays = 366
	require.Error(t, validateOpsAdvancedSettings(cfg))

	// 旧客户端不带这个字段（解析为 0）：校验放行，规范化后为默认 5 天。
	cfg.DataRetention.QueueRejectRetentionDays = 0
	require.NoError(t, validateOpsAdvancedSettings(cfg))
	normalizeOpsAdvancedSettings(cfg)
	require.Equal(t, 5, cfg.DataRetention.QueueRejectRetentionDays)
}
