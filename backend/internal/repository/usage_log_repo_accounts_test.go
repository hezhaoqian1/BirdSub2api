package repository

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestAccountStatsCostsAndFilters(t *testing.T) {
	db, mock := newSQLMock(t)
	repo := &usageLogRepository{sql: db}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	requestType, billingType := int16(2), int8(1)
	compact, mismatch := true, true
	filters := usagestats.UsageLogFilters{UserID: 3, APIKeyID: 4, AccountID: 5, GroupID: 6, Model: "requested-model", RequestType: &requestType, NativeCompactionV2: &compact, BillingType: &billingType, UpstreamModelMismatch: &mismatch}
	// Historical per-request pricing must be applied before summing, and names
	// must be left-joined so deleted accounts do not drop historical charges.
	pattern := `(?s)` + regexp.QuoteMeta("SUM(COALESCE(ul.account_stats_cost, ul.total_cost) * COALESCE(ul.account_rate_multiplier, 1))") + `.*LEFT JOIN accounts a ON a.id = ul.account_id.*ul.created_at >= \$1 AND ul.created_at < \$2.*ul.user_id = \$3.*ul.api_key_id = \$4.*ul.account_id = \$5.*ul.group_id = \$6.*` + regexp.QuoteMeta("COALESCE(NULLIF(TRIM(ul.requested_model), ''), ul.model) = $7") + `.*native_compaction_v2.*billing_type.*upstream_model_mismatch.*GROUP BY ul.account_id, a.name ORDER BY account_cost DESC`
	mock.ExpectQuery(pattern).WithArgs(start, end, int64(3), int64(4), int64(5), int64(6), "requested-model", requestType, compact, int16(billingType)).WillReturnRows(sqlmock.NewRows([]string{"account_id", "account_name", "requests", "total_tokens", "cost", "actual_cost", "account_cost"}).AddRow(5, "upstream", 2, 1000, 10.0, 7.0, 3.5))
	rows, err := repo.GetAccountStatsWithUsageFilters(context.Background(), start, end, filters)
	require.NoError(t, err)
	require.Equal(t, []usagestats.AccountStat{{AccountID: 5, AccountName: "upstream", Requests: 2, TotalTokens: 1000, Cost: 10, ActualCost: 7, AccountCost: 3.5}}, rows)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAccountStatsEmptyAndDeletedAccount(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(map[bool]string{true: "empty", false: "deleted account"}[empty], func(t *testing.T) {
			db, mock := newSQLMock(t)
			repo := &usageLogRepository{sql: db}
			start := time.Now()
			rows := sqlmock.NewRows([]string{"account_id", "account_name", "requests", "total_tokens", "cost", "actual_cost", "account_cost"})
			if !empty {
				rows.AddRow(99, "", 1, 40, 2.0, 1.0, 0.5)
			}
			mock.ExpectQuery("FROM usage_logs").WithArgs(start, start.Add(time.Hour)).WillReturnRows(rows)
			result, err := repo.GetAccountStatsWithUsageFilters(context.Background(), start, start.Add(time.Hour), usagestats.UsageLogFilters{})
			require.NoError(t, err)
			require.NotNil(t, result)
			if empty {
				require.Empty(t, result)
			} else {
				require.Equal(t, int64(99), result[0].AccountID)
				require.Equal(t, 0.5, result[0].AccountCost)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
