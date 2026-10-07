package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type profitStatsRepository struct {
	sql sqlExecutor
}

// NewProfitStatsRepository 创建利润统计仓储。利润统计直接扫描 usage_logs：
// 仪表盘预聚合表没有用户维度，无法排除管理员流量。
func NewProfitStatsRepository(sqlDB *sql.DB) service.ProfitStatsRepository {
	return &profitStatsRepository{sql: sqlDB}
}

// profitAccountCostExpr 与仪表盘 account_cost 口径一致。
const profitAccountCostExpr = "COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)"

// profitSubscriptionAccountTypes 是按月付费、不按 token 计成本的账号类型。
var profitSubscriptionAccountTypesSQL = fmt.Sprintf("('%s', '%s')", service.AccountTypeOAuth, service.AccountTypeSetupToken)

func profitStatsTimezone(name string) string {
	if name != "" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	return timezone.Name()
}

func (r *profitStatsRepository) QueryProfitStats(ctx context.Context, filter service.ProfitStatsFilter) (results []service.ProfitStatsRawRow, err error) {
	args := []any{filter.StartTime, filter.EndTime, profitStatsTimezone(filter.Timezone)}
	where := "ul.created_at >= $1 AND ul.created_at < $2"
	if filter.ExcludeAdmin {
		where += fmt.Sprintf(" AND NOT EXISTS (SELECT 1 FROM users au WHERE au.id = ul.user_id AND au.role = '%s')", service.RoleAdmin)
	}
	if filter.GroupID > 0 {
		args = append(args, filter.GroupID)
		where += fmt.Sprintf(" AND ul.group_id = $%d", len(args))
	}

	query := fmt.Sprintf(`
		WITH base AS (
			SELECT
				(ul.created_at AT TIME ZONE $3)::date AS day,
				COALESCE(ul.group_id, 0) AS group_id,
				COALESCE(ul.account_id, 0) AS account_id,
				%s AS model,
				ul.user_id,
				COALESCE(a.type IN %s, false) AS subscription,
				(ul.input_tokens + ul.output_tokens + ul.cache_creation_tokens + ul.cache_read_tokens) AS tokens,
				ul.total_cost,
				ul.actual_cost,
				%s AS account_cost
			FROM usage_logs ul
			LEFT JOIN accounts a ON a.id = ul.account_id
			WHERE %s
		),
		agg AS (
			SELECT
				GROUPING(day) AS g_day,
				GROUPING(group_id) AS g_group,
				GROUPING(account_id) AS g_account,
				GROUPING(model) AS g_model,
				GROUPING(user_id) AS g_user,
				day, group_id, account_id, model, user_id, subscription,
				COUNT(*) AS requests,
				COALESCE(SUM(tokens), 0) AS tokens,
				COALESCE(SUM(total_cost), 0) AS standard_cost,
				COALESCE(SUM(actual_cost), 0) AS revenue,
				COALESCE(SUM(account_cost), 0) AS cost
			FROM base
			GROUP BY GROUPING SETS (
				(subscription),
				(day, subscription),
				(group_id, subscription),
				(account_id, subscription),
				(model, subscription),
				(user_id, subscription)
			)
		)
		SELECT
			CASE
				WHEN agg.g_day = 0 THEN '%s'
				WHEN agg.g_group = 0 THEN '%s'
				WHEN agg.g_account = 0 THEN '%s'
				WHEN agg.g_model = 0 THEN '%s'
				WHEN agg.g_user = 0 THEN '%s'
				ELSE '%s'
			END AS dimension,
			COALESCE(TO_CHAR(agg.day, 'YYYY-MM-DD'), '') AS day,
			COALESCE(agg.group_id, 0) AS group_id,
			COALESCE(g.name, '') AS group_name,
			COALESCE(agg.account_id, 0) AS account_id,
			COALESCE(agg.model, '') AS model,
			COALESCE(agg.user_id, 0) AS user_id,
			COALESCE(u.email, '') AS user_email,
			agg.subscription,
			agg.requests,
			agg.tokens,
			agg.standard_cost,
			agg.revenue,
			agg.cost
		FROM agg
		LEFT JOIN groups g ON agg.g_group = 0 AND g.id = agg.group_id
		LEFT JOIN users u ON agg.g_user = 0 AND u.id = agg.user_id
	`,
		resolveModelDimensionExpressionWithAlias("", "ul"),
		profitSubscriptionAccountTypesSQL,
		profitAccountCostExpr,
		where,
		service.ProfitDimensionDay,
		service.ProfitDimensionGroup,
		service.ProfitDimensionAccount,
		service.ProfitDimensionModel,
		service.ProfitDimensionUser,
		service.ProfitDimensionTotal,
	)

	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]service.ProfitStatsRawRow, 0)
	for rows.Next() {
		var row service.ProfitStatsRawRow
		if err = rows.Scan(
			&row.Dimension,
			&row.Day,
			&row.GroupID,
			&row.GroupName,
			&row.AccountID,
			&row.Model,
			&row.UserID,
			&row.UserEmail,
			&row.Subscription,
			&row.Requests,
			&row.Tokens,
			&row.StandardCost,
			&row.Revenue,
			&row.Cost,
		); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// profitBackfillCondition 选出仍为默认倍率 1、而账号当前成本倍率不为 1 的按量账号记录。
var profitBackfillCondition = fmt.Sprintf(`
	ul.created_at >= $1 AND ul.created_at < $2
	AND a.type NOT IN %s
	AND a.rate_multiplier <> 1
	AND COALESCE(ul.account_rate_multiplier, 1) = 1
`, profitSubscriptionAccountTypesSQL)

func (r *profitStatsRepository) PreviewCostRateBackfill(ctx context.Context, start, end time.Time) (results []service.ProfitCostBackfillCandidate, err error) {
	query := fmt.Sprintf(`
		SELECT
			a.id,
			a.name,
			a.platform,
			a.rate_multiplier::float8,
			COUNT(*) AS rows,
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1)), 0) AS old_cost,
			COALESCE(SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * a.rate_multiplier), 0) AS new_cost
		FROM usage_logs ul
		JOIN accounts a ON a.id = ul.account_id
		WHERE %s
		GROUP BY a.id, a.name, a.platform, a.rate_multiplier
		ORDER BY old_cost DESC
	`, profitBackfillCondition)

	rows, err := r.sql.QueryContext(ctx, query, start, end)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
			results = nil
		}
	}()

	results = make([]service.ProfitCostBackfillCandidate, 0)
	for rows.Next() {
		var row service.ProfitCostBackfillCandidate
		if err = rows.Scan(&row.AccountID, &row.AccountName, &row.Platform, &row.Rate, &row.Rows, &row.OldCost, &row.NewCost); err != nil {
			return nil, err
		}
		results = append(results, row)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (r *profitStatsRepository) ApplyCostRateBackfill(ctx context.Context, start, end time.Time, accountID int64) (int64, error) {
	query := fmt.Sprintf(`
		UPDATE usage_logs ul
		SET account_rate_multiplier = a.rate_multiplier
		FROM accounts a
		WHERE a.id = ul.account_id
			AND ul.account_id = $3
			AND %s
	`, profitBackfillCondition)
	result, err := r.sql.ExecContext(ctx, query, start, end, accountID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
