//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type profitStatsFixture struct {
	start, end     time.Time
	groupID        int64
	meteredID      int64
	subscriptionID int64
	customerID     int64
	adminID        int64
}

// seedProfitStatsFixture 在一个与其它集成测试不重叠的时间窗口内写入：
//   - 按量账号（成本倍率 0.2，历史记录快照仍为 1），客户与管理员各两条；
//   - 订阅号（OAuth）一条客户记录。
func seedProfitStatsFixture(t *testing.T, label string) profitStatsFixture {
	t.Helper()
	ctx := context.Background()
	client := integrationEntClient

	customer := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("profit-customer-%s@test.com", label)})
	admin := mustCreateUser(t, client, &service.User{Email: fmt.Sprintf("profit-admin-%s@test.com", label), Role: service.RoleAdmin})
	group := mustCreateGroup(t, client, &service.Group{Name: "profit-group-" + label})
	metered := mustCreateAccount(t, client, &service.Account{Name: "profit-metered-" + label, Type: service.AccountTypeAPIKey})
	subscription := mustCreateAccount(t, client, &service.Account{Name: "profit-sub-" + label, Type: service.AccountTypeOAuth})
	_, err := integrationDB.ExecContext(ctx, "UPDATE accounts SET rate_multiplier = 0.2 WHERE id = $1", metered.ID)
	require.NoError(t, err)

	customerKey := mustCreateApiKey(t, client, &service.APIKey{UserID: customer.ID, Key: "sk-profit-customer-" + label})
	adminKey := mustCreateApiKey(t, client, &service.APIKey{UserID: admin.ID, Key: "sk-profit-admin-" + label})

	// 2031-03-10 23:30 UTC = 2031-03-11 07:30 Asia/Shanghai，用于验证按时区分日。
	base := time.Date(2031, 3, 10, 23, 30, 0, 0, time.UTC)
	seq := 0
	insert := func(userID, apiKeyID, accountID int64, model string, totalCost, actualCost float64, at time.Time) {
		seq++
		_, err := client.UsageLog.Create().
			SetUserID(userID).
			SetAPIKeyID(apiKeyID).
			SetAccountID(accountID).
			SetGroupID(group.ID).
			SetRequestID(fmt.Sprintf("profit-%s-%d", label, seq)).
			SetModel(model).
			SetInputTokens(100).
			SetOutputTokens(10).
			SetTotalCost(totalCost).
			SetActualCost(actualCost).
			SetRateMultiplier(0.3).
			SetAccountRateMultiplier(1).
			SetCreatedAt(at).
			Save(ctx)
		require.NoError(t, err)
	}
	insert(customer.ID, customerKey.ID, metered.ID, "gpt-a", 10, 3, base)
	insert(customer.ID, customerKey.ID, metered.ID, "gpt-b", 20, 6, base.Add(2*time.Hour))
	insert(admin.ID, adminKey.ID, metered.ID, "gpt-a", 100, 30, base.Add(time.Hour))
	insert(customer.ID, customerKey.ID, subscription.ID, "gpt-a", 50, 15, base.Add(3*time.Hour))

	return profitStatsFixture{
		start:          base.Add(-24 * time.Hour),
		end:            base.Add(24 * time.Hour),
		groupID:        group.ID,
		meteredID:      metered.ID,
		subscriptionID: subscription.ID,
		customerID:     customer.ID,
		adminID:        admin.ID,
	}
}

func profitRows(rows []service.ProfitStatsRawRow, dimension string, subscription bool) []service.ProfitStatsRawRow {
	out := make([]service.ProfitStatsRawRow, 0)
	for _, row := range rows {
		if row.Dimension == dimension && row.Subscription == subscription {
			out = append(out, row)
		}
	}
	return out
}

func TestProfitStatsRepository_QueryAndBackfill(t *testing.T) {
	ctx := context.Background()
	fx := seedProfitStatsFixture(t, "query")
	repo := NewProfitStatsRepository(integrationDB)

	filter := service.ProfitStatsFilter{StartTime: fx.start, EndTime: fx.end, Timezone: "Asia/Shanghai", GroupID: fx.groupID}

	t.Run("includes admin by default and splits subscription traffic", func(t *testing.T) {
		rows, err := repo.QueryProfitStats(ctx, filter)
		require.NoError(t, err)

		total := profitRows(rows, service.ProfitDimensionTotal, false)
		require.Len(t, total, 1)
		require.Equal(t, int64(3), total[0].Requests)
		require.InDelta(t, 39, total[0].Revenue, 1e-9)
		require.InDelta(t, 130, total[0].Cost, 1e-9) // 历史快照仍为 1

		sub := profitRows(rows, service.ProfitDimensionTotal, true)
		require.Len(t, sub, 1)
		require.InDelta(t, 15, sub[0].Revenue, 1e-9)

		days := profitRows(rows, service.ProfitDimensionDay, false)
		require.Len(t, days, 1)
		require.Equal(t, "2031-03-11", days[0].Day)

		groups := profitRows(rows, service.ProfitDimensionGroup, false)
		require.Len(t, groups, 1)
		require.Equal(t, "profit-group-query", groups[0].GroupName)

		users := profitRows(rows, service.ProfitDimensionUser, false)
		require.Len(t, users, 2)
		models := profitRows(rows, service.ProfitDimensionModel, false)
		require.Len(t, models, 2)
		accounts := append(profitRows(rows, service.ProfitDimensionAccount, false), profitRows(rows, service.ProfitDimensionAccount, true)...)
		require.Len(t, accounts, 2)
	})

	t.Run("excludes admin traffic", func(t *testing.T) {
		excluded := filter
		excluded.ExcludeAdmin = true
		rows, err := repo.QueryProfitStats(ctx, excluded)
		require.NoError(t, err)
		total := profitRows(rows, service.ProfitDimensionTotal, false)
		require.Len(t, total, 1)
		require.Equal(t, int64(2), total[0].Requests)
		require.InDelta(t, 9, total[0].Revenue, 1e-9)
		for _, row := range profitRows(rows, service.ProfitDimensionUser, false) {
			require.NotEqual(t, fx.adminID, row.UserID)
		}
	})

	t.Run("backfill rewrites only default-rate metered rows", func(t *testing.T) {
		candidates, err := repo.PreviewCostRateBackfill(ctx, fx.start, fx.end)
		require.NoError(t, err)
		var candidate *service.ProfitCostBackfillCandidate
		for i := range candidates {
			require.NotEqual(t, fx.subscriptionID, candidates[i].AccountID)
			if candidates[i].AccountID == fx.meteredID {
				candidate = &candidates[i]
			}
		}
		require.NotNil(t, candidate)
		require.Equal(t, int64(3), candidate.Rows)
		require.InDelta(t, 0.2, candidate.Rate, 1e-9)
		require.InDelta(t, 130, candidate.OldCost, 1e-9)
		require.InDelta(t, 26, candidate.NewCost, 1e-9)

		affected, err := repo.ApplyCostRateBackfill(ctx, fx.start, fx.end, fx.meteredID)
		require.NoError(t, err)
		require.Equal(t, int64(3), affected)

		rows, err := repo.QueryProfitStats(ctx, filter)
		require.NoError(t, err)
		total := profitRows(rows, service.ProfitDimensionTotal, false)
		require.InDelta(t, 26, total[0].Cost, 1e-9)

		// 回填后再改倍率，已回填记录不再是默认倍率，不会被二次改写。
		_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET rate_multiplier = 0.5 WHERE id = $1", fx.meteredID)
		require.NoError(t, err)
		affected, err = repo.ApplyCostRateBackfill(ctx, fx.start, fx.end, fx.meteredID)
		require.NoError(t, err)
		require.Zero(t, affected)
	})
}
