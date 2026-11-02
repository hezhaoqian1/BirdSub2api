package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	opsCleanupDefaultSchedule  = "0 2 * * *"
	opsCleanupBatchSize        = 5000
	opsCleanupCronStopTimeout  = 3 * time.Second
	opsCleanupRunTimeout       = 30 * time.Minute
	opsCleanupHeartbeatTimeout = 2 * time.Second
)

type opsCleanupTarget struct {
	retentionDays int
	table         string
	timeCol       string
	castDate      bool
	counter       *int64
}

type opsCleanupDeletedCounts struct {
	errorLogs      int64
	ingressRejects int64
	alertEvents    int64
	systemLogs     int64
	logAudits      int64
	systemMetrics  int64
	hourlyPreagg   int64
	dailyPreagg    int64
}

func (c opsCleanupDeletedCounts) String() string {
	return fmt.Sprintf(
		"error_logs=%d ingress_rejects=%d alert_events=%d system_logs=%d log_audits=%d system_metrics=%d hourly_preagg=%d daily_preagg=%d",
		c.errorLogs,
		c.ingressRejects,
		c.alertEvents,
		c.systemLogs,
		c.logAudits,
		c.systemMetrics,
		c.hourlyPreagg,
		c.dailyPreagg,
	)
}

// opsCleanupPlan 把"保留天数"翻译成具体的清理动作。
//   - days < 0  → 跳过该项清理（ok=false），保留兼容老数据
//   - days == 0 → TRUNCATE TABLE（O(1) 全清），truncate=true
//   - days > 0  → 批量 DELETE 早于 now-N天 的行，cutoff = now - N 天
func opsCleanupPlan(now time.Time, days int) (cutoff time.Time, truncate, ok bool) {
	if days < 0 {
		return time.Time{}, false, false
	}
	if days == 0 {
		return time.Time{}, true, true
	}
	return now.AddDate(0, 0, -days), false, true
}

func opsCleanupRunOne(
	ctx context.Context,
	db *sql.DB,
	truncate bool,
	cutoff time.Time,
	table, timeCol string,
	castDate bool,
	batchSize int,
) (int64, error) {
	if truncate {
		return truncateOpsTable(ctx, db, table)
	}
	return deleteOldRowsByID(ctx, db, table, timeCol, cutoff, batchSize, castDate)
}

func deleteOldRowsByID(
	ctx context.Context,
	db *sql.DB,
	table string,
	timeColumn string,
	cutoff time.Time,
	batchSize int,
	castCutoffToDate bool,
) (int64, error) {
	return deleteOldRowsByIDWhere(ctx, db, table, timeColumn, "", cutoff, batchSize, castCutoffToDate)
}

// deleteOldRowsByIDWhere 与 deleteOldRowsByID 相同，额外加一个固定的过滤条件（只用常量 SQL）。
func deleteOldRowsByIDWhere(
	ctx context.Context,
	db *sql.DB,
	table string,
	timeColumn string,
	extraWhere string,
	cutoff time.Time,
	batchSize int,
	castCutoffToDate bool,
) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if batchSize <= 0 {
		batchSize = opsCleanupBatchSize
	}

	where := fmt.Sprintf("%s < $1", timeColumn)
	if castCutoffToDate {
		where = fmt.Sprintf("%s < $1::date", timeColumn)
	}
	if extraWhere != "" {
		where += " AND (" + extraWhere + ")"
	}

	q := fmt.Sprintf(`
WITH batch AS (
  SELECT id FROM %s
  WHERE %s
  ORDER BY id
  LIMIT $2
)
DELETE FROM %s
WHERE id IN (SELECT id FROM batch)
`, table, where, table)

	var total int64
	for {
		res, err := db.ExecContext(ctx, q, cutoff, batchSize)
		if err != nil {
			if isMissingRelationError(err) {
				return total, nil
			}
			return total, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
		if affected == 0 {
			break
		}
	}
	return total, nil
}

const (
	opsQueueRejectDefaultRetentionDays = 5
	// 改为聚合之前逐条写入 ops_error_logs 的容量拒绝：
	//   - 网关排队满（Phase=request、业务限制、429）；
	//   - 纯路由的"无可用账号"（Phase=routing、503、没有任何上游尝试）。
	opsQueueRejectErrorLogFilter = `(error_phase = 'request' AND is_business_limited AND status_code = 429 AND error_message LIKE 'Too many pending requests%')` +
		` OR (error_phase = 'routing' AND status_code = 503 AND error_message LIKE 'No available%' AND (upstream_errors IS NULL OR upstream_errors::text IN ('null', '[]')))`
	opsQueueRejectAggregateFilter = `reject_reason IN ('gateway_queue_full', 'no_available_accounts')`
)

// cleanupOpsQueueRejects 按 queueDays 删除容量拒绝记录（排队满、无可用账号）。普通错误日志保留更短（或为 0 时整表清空）时
// 已经由常规清理覆盖，不再重复删除。
func cleanupOpsQueueRejects(ctx context.Context, db *sql.DB, now time.Time, queueDays, errorLogDays int) (errorLogs, aggregates int64, err error) {
	if db == nil || queueDays <= 0 || errorLogDays == 0 || (errorLogDays > 0 && queueDays >= errorLogDays) {
		return 0, 0, nil
	}
	cutoff := now.AddDate(0, 0, -queueDays)
	errorLogs, err = deleteOldRowsByIDWhere(ctx, db, "ops_error_logs", "created_at", opsQueueRejectErrorLogFilter, cutoff, opsCleanupBatchSize, false)
	if err != nil {
		return errorLogs, 0, err
	}
	aggregates, err = deleteOldRowsByIDWhere(ctx, db, "ops_ingress_reject_aggregates", "bucket_start", opsQueueRejectAggregateFilter, cutoff, opsCleanupBatchSize, false)
	return errorLogs, aggregates, err
}

// truncateOpsTable 用 TRUNCATE TABLE 清空指定表，先 SELECT COUNT(*) 取得清空前行数用于 heartbeat。
func truncateOpsTable(ctx context.Context, db *sql.DB, table string) (int64, error) {
	if db == nil {
		return 0, nil
	}
	var count int64
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
		if isMissingRelationError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	if count == 0 {
		return 0, nil
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("TRUNCATE TABLE %s", table)); err != nil {
		if isMissingRelationError(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("truncate %s: %w", table, err)
	}
	return count, nil
}

func isMissingRelationError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "does not exist") && strings.Contains(s, "relation")
}
