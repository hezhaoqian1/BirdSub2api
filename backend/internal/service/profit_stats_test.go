package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type profitStatsRepoFake struct {
	rows       []ProfitStatsRawRow
	candidates []ProfitCostBackfillCandidate
	applied    []int64
}

func (f *profitStatsRepoFake) QueryProfitStats(context.Context, ProfitStatsFilter) ([]ProfitStatsRawRow, error) {
	return f.rows, nil
}

func (f *profitStatsRepoFake) PreviewCostRateBackfill(context.Context, time.Time, time.Time) ([]ProfitCostBackfillCandidate, error) {
	return f.candidates, nil
}

func (f *profitStatsRepoFake) ApplyCostRateBackfill(_ context.Context, _, _ time.Time, accountID int64) (int64, error) {
	f.applied = append(f.applied, accountID)
	return 10, nil
}

type profitAccountRepoFake struct {
	AccountRepository
	accounts []*Account
}

func (f *profitAccountRepoFake) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return f.accounts, nil
}

type profitSettingRepoFake struct {
	SettingRepository
	values map[string]string
}

func (f *profitSettingRepoFake) GetValue(_ context.Context, key string) (string, error) {
	value, ok := f.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (f *profitSettingRepoFake) Set(_ context.Context, key, value string) error {
	f.values[key] = value
	return nil
}

func profitRate(v float64) *float64 { return &v }

func TestProfitAccountCostStatus(t *testing.T) {
	cases := []struct {
		name string
		row  ProfitAccountRow
		sub  bool
		want string
	}{
		{"configured rate", ProfitAccountRow{Type: AccountTypeAPIKey, CostRate: 0.2}, false, ProfitCostStatusConfigured},
		{"default rate without probe", ProfitAccountRow{Type: AccountTypeAPIKey, CostRate: 1}, false, ProfitCostStatusUnconfigured},
		{"default rate probe says 0.25", ProfitAccountRow{Type: AccountTypeAPIKey, CostRate: 1, ProbeRate: profitRate(0.25)}, false, ProfitCostStatusUnconfigured},
		{"default rate confirmed by probe", ProfitAccountRow{Type: AccountTypeAPIKey, CostRate: 1, ProbeRate: profitRate(1)}, false, ProfitCostStatusConfigured},
		{"oauth account", ProfitAccountRow{Type: AccountTypeOAuth, CostRate: 1}, false, ProfitCostStatusSubscription},
		{"subscription flag from query", ProfitAccountRow{CostRate: 1, Deleted: true}, true, ProfitCostStatusSubscription},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, profitAccountCostStatus(tc.row, tc.sub))
		})
	}
}

func TestProfitStatsServiceGetReport(t *testing.T) {
	repo := &profitStatsRepoFake{rows: []ProfitStatsRawRow{
		{Dimension: ProfitDimensionTotal, Requests: 3, StandardCost: 130, Revenue: 39, Cost: 26},
		{Dimension: ProfitDimensionTotal, Subscription: true, Requests: 1, StandardCost: 50, Revenue: 15, Cost: 50},
		{Dimension: ProfitDimensionDay, Day: "2026-10-02", Revenue: 9, Cost: 6},
		{Dimension: ProfitDimensionDay, Day: "2026-10-01", Revenue: 30, Cost: 20},
		{Dimension: ProfitDimensionDay, Subscription: true, Day: "2026-10-01", Revenue: 15, Cost: 50},
		{Dimension: ProfitDimensionGroup, GroupID: 7, GroupName: "g", Revenue: 39, Cost: 26},
		{Dimension: ProfitDimensionAccount, AccountID: 1, Revenue: 30, Cost: 20},
		{Dimension: ProfitDimensionAccount, AccountID: 2, Revenue: 9, Cost: 6},
		{Dimension: ProfitDimensionAccount, AccountID: 3, Subscription: true, Revenue: 15, Cost: 50},
		{Dimension: ProfitDimensionUser, UserID: 5, UserEmail: "a@b.c", Revenue: 39, Cost: 26},
	}}
	accounts := &profitAccountRepoFake{accounts: []*Account{
		{ID: 1, Name: "sixoner", Type: AccountTypeAPIKey, RateMultiplier: profitRate(0.2)},
		{ID: 2, Name: "zzone", Type: AccountTypeAPIKey},
		{ID: 3, Name: "oauth", Type: AccountTypeOAuth},
	}}
	svc := NewProfitStatsService(repo, accounts, &profitSettingRepoFake{values: map[string]string{}}, nil)

	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	report, err := svc.GetReport(context.Background(), ProfitStatsFilter{StartTime: start, EndTime: start.AddDate(0, 0, 7), ExcludeAdmin: true})
	require.NoError(t, err)

	require.InDelta(t, 13, report.Summary.Profit, 1e-9)
	require.NotNil(t, report.Summary.ProfitMargin)
	require.InDelta(t, 13.0/39.0, *report.Summary.ProfitMargin, 1e-9)
	require.InDelta(t, 15, report.Subscription.Revenue, 1e-9)
	require.Zero(t, report.Subscription.Cost)

	require.Len(t, report.Daily, 2)
	require.Equal(t, "2026-10-01", report.Daily[0].Date)
	require.InDelta(t, 30, report.Daily[0].Revenue, 1e-9, "subscription traffic must not enter daily profit")

	require.Len(t, report.ByAccount, 3)
	require.Equal(t, int64(1), report.ByAccount[0].AccountID)
	require.Equal(t, ProfitCostStatusConfigured, report.ByAccount[0].CostStatus)
	byID := map[int64]ProfitAccountRow{}
	for _, row := range report.ByAccount {
		byID[row.AccountID] = row
	}
	require.Equal(t, ProfitCostStatusUnconfigured, byID[2].CostStatus)
	require.Equal(t, ProfitCostStatusSubscription, byID[3].CostStatus)
	require.Zero(t, byID[3].Cost)
	require.InDelta(t, 9, report.UnconfiguredRevenue, 1e-9)
}

func TestProfitStatsServiceRejectsLargeRange(t *testing.T) {
	svc := NewProfitStatsService(&profitStatsRepoFake{}, nil, nil, nil)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := svc.GetReport(context.Background(), ProfitStatsFilter{StartTime: start, EndTime: start.AddDate(0, 6, 0)})
	require.ErrorIs(t, err, ErrProfitRangeTooLarge)
}

func TestProfitStatsServiceBackfillRunsOnce(t *testing.T) {
	repo := &profitStatsRepoFake{candidates: []ProfitCostBackfillCandidate{{AccountID: 1}, {AccountID: 2}, {AccountID: 3}}}
	settings := &profitSettingRepoFake{values: map[string]string{}}
	svc := NewProfitStatsService(repo, nil, settings, nil)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	state, err := svc.ApplyCostRateBackfill(context.Background(), start, []int64{1, 3})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 3}, repo.applied)
	require.Equal(t, int64(20), state.Rows)
	require.Equal(t, now, state.EndTime)
	require.Contains(t, settings.values, SettingKeyProfitCostBackfillState)

	_, err = svc.ApplyCostRateBackfill(context.Background(), start, nil)
	require.ErrorIs(t, err, ErrProfitBackfillApplied)
	require.Equal(t, []int64{1, 3}, repo.applied, "second run must not touch history")

	preview, err := svc.PreviewCostRateBackfill(context.Background(), start)
	require.NoError(t, err)
	require.NotNil(t, preview.Applied)
	require.Equal(t, int64(20), preview.Applied.Rows)
}
