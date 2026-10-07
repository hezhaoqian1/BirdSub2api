package service

// 利润统计：收入 = 用户实际扣费（actual_cost），成本 = 官方价 × 请求时账号成本倍率快照
// （与仪表盘 account_cost 同一口径），毛利 = 收入 − 成本。
//
// 口径约定：
//   - 订阅号（OAuth / Setup Token 账号）按月付费、成本不随 token 变化，暂不计入利润，
//     其收入单独汇总展示；
//   - 可选排除管理员自用流量；
//   - 成本倍率在请求时快照到 usage_logs.account_rate_multiplier，后续修改账号倍率
//     只影响之后的请求。历史数据只能通过一次性回填修正（见 ApplyCostRateBackfill）。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	ProfitDimensionTotal   = "total"
	ProfitDimensionDay     = "day"
	ProfitDimensionGroup   = "group"
	ProfitDimensionAccount = "account"
	ProfitDimensionModel   = "model"
	ProfitDimensionUser    = "user"

	ProfitCostStatusConfigured   = "configured"
	ProfitCostStatusUnconfigured = "unconfigured"
	ProfitCostStatusSubscription = "subscription"

	// SettingKeyProfitCostBackfillState 记录一次性历史成本回填的执行结果，存在即视为已执行。
	SettingKeyProfitCostBackfillState = "profit_cost_rate_backfill_state"

	profitStatsMaxRangeDays    = 93
	profitBackfillMaxRangeDays = 93
)

var (
	ErrProfitRangeTooLarge      = infraerrors.BadRequest("PROFIT_RANGE_TOO_LARGE", "date range too large")
	ErrProfitBackfillApplied    = infraerrors.Conflict("PROFIT_BACKFILL_ALREADY_APPLIED", "historical cost backfill has already been applied")
	ErrProfitBackfillInProgress = infraerrors.Conflict("PROFIT_BACKFILL_IN_PROGRESS", "historical cost backfill is running")
)

// ProfitStatsFilter 是利润统计的查询条件。
type ProfitStatsFilter struct {
	StartTime    time.Time
	EndTime      time.Time
	Timezone     string
	ExcludeAdmin bool
	GroupID      int64
}

// ProfitStatsRawRow 是仓储按 GROUPING SETS 返回的一行聚合结果。
type ProfitStatsRawRow struct {
	Dimension    string
	Day          string
	GroupID      int64
	GroupName    string
	AccountID    int64
	Model        string
	UserID       int64
	UserEmail    string
	Subscription bool
	Requests     int64
	Tokens       int64
	StandardCost float64
	Revenue      float64
	Cost         float64
}

// ProfitCostBackfillCandidate 是单账号的历史成本回填预览。
type ProfitCostBackfillCandidate struct {
	AccountID   int64   `json:"account_id"`
	AccountName string  `json:"account_name"`
	Platform    string  `json:"platform"`
	Rate        float64 `json:"rate"`
	Rows        int64   `json:"rows"`
	OldCost     float64 `json:"old_cost"`
	NewCost     float64 `json:"new_cost"`
}

// ProfitStatsRepository 提供利润统计所需的原始聚合与回填能力。
type ProfitStatsRepository interface {
	QueryProfitStats(ctx context.Context, filter ProfitStatsFilter) ([]ProfitStatsRawRow, error)
	PreviewCostRateBackfill(ctx context.Context, start, end time.Time) ([]ProfitCostBackfillCandidate, error)
	ApplyCostRateBackfill(ctx context.Context, start, end time.Time, accountID int64) (int64, error)
}

// ProfitAmounts 是一组金额汇总。ProfitMargin 在收入为 0 时为 nil。
type ProfitAmounts struct {
	Requests     int64    `json:"requests"`
	Tokens       int64    `json:"tokens"`
	StandardCost float64  `json:"standard_cost"`
	Revenue      float64  `json:"revenue"`
	Cost         float64  `json:"cost"`
	Profit       float64  `json:"profit"`
	ProfitMargin *float64 `json:"profit_margin"`
}

func (a *ProfitAmounts) add(row ProfitStatsRawRow) {
	a.Requests += row.Requests
	a.Tokens += row.Tokens
	a.StandardCost += row.StandardCost
	a.Revenue += row.Revenue
	a.Cost += row.Cost
	a.finalize()
}

func (a *ProfitAmounts) finalize() {
	a.Profit = a.Revenue - a.Cost
	a.ProfitMargin = nil
	if a.Revenue > 0 {
		margin := a.Profit / a.Revenue
		a.ProfitMargin = &margin
	}
}

type ProfitDailyRow struct {
	Date string `json:"date"`
	ProfitAmounts
}

type ProfitGroupRow struct {
	GroupID   int64  `json:"group_id"`
	GroupName string `json:"group_name"`
	ProfitAmounts
}

type ProfitModelRow struct {
	Model string `json:"model"`
	ProfitAmounts
}

type ProfitUserRow struct {
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
	ProfitAmounts
}

type ProfitAccountRow struct {
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name"`
	Platform    string `json:"platform"`
	Type        string `json:"type"`
	// CostRate 为账号当前成本倍率（只影响之后的请求）。
	CostRate float64 `json:"cost_rate"`
	// RateSyncEnabled 表示成本倍率由上游探测自动维护，手工修改会被拒绝。
	RateSyncEnabled bool `json:"rate_sync_enabled,omitempty"`
	// ProbeRate 为上游计费探测声明的基准倍率（不含高峰），仅上游为 sub2api 时可得。
	ProbeRate   *float64 `json:"probe_rate,omitempty"`
	ProbeStatus string   `json:"probe_status,omitempty"`
	CostStatus  string   `json:"cost_status"`
	Deleted     bool     `json:"deleted,omitempty"`
	ProfitAmounts
}

// ProfitCostBackfillState 是一次性回填的执行记录。
type ProfitCostBackfillState struct {
	AppliedAt  time.Time `json:"applied_at"`
	StartTime  time.Time `json:"start_time"`
	EndTime    time.Time `json:"end_time"`
	AccountIDs []int64   `json:"account_ids"`
	Rows       int64     `json:"rows"`
}

type ProfitReport struct {
	StartTime    time.Time `json:"start_time"`
	EndTime      time.Time `json:"end_time"`
	ExcludeAdmin bool      `json:"exclude_admin"`
	// Summary 只含按量（非订阅号）流量。
	Summary ProfitAmounts `json:"summary"`
	// Subscription 为订阅号流量，成本不计，仅展示收入。
	Subscription ProfitAmounts `json:"subscription"`
	// UnconfiguredRevenue 为成本倍率疑似未配置账号上的收入，用于提示统计可信度。
	UnconfiguredRevenue float64                  `json:"unconfigured_revenue"`
	Daily               []ProfitDailyRow         `json:"daily"`
	ByGroup             []ProfitGroupRow         `json:"by_group"`
	ByAccount           []ProfitAccountRow       `json:"by_account"`
	ByModel             []ProfitModelRow         `json:"by_model"`
	ByUser              []ProfitUserRow          `json:"by_user"`
	Backfill            *ProfitCostBackfillState `json:"backfill,omitempty"`
}

type ProfitCostBackfillPreview struct {
	StartTime  time.Time                     `json:"start_time"`
	EndTime    time.Time                     `json:"end_time"`
	Applied    *ProfitCostBackfillState      `json:"applied,omitempty"`
	Candidates []ProfitCostBackfillCandidate `json:"candidates"`
}

// ProfitStatsService 汇总利润统计并负责一次性历史成本回填。
type ProfitStatsService struct {
	repo        ProfitStatsRepository
	accountRepo AccountRepository
	settingRepo SettingRepository
	aggregation *DashboardAggregationService
	now         func() time.Time
	backfillMu  sync.Mutex
}

func NewProfitStatsService(repo ProfitStatsRepository, accountRepo AccountRepository, settingRepo SettingRepository, aggregation *DashboardAggregationService) *ProfitStatsService {
	return &ProfitStatsService{
		repo:        repo,
		accountRepo: accountRepo,
		settingRepo: settingRepo,
		aggregation: aggregation,
		now:         time.Now,
	}
}

func validateProfitRange(start, end time.Time, maxDays int) error {
	if !end.After(start) {
		return infraerrors.BadRequest("PROFIT_RANGE_INVALID", "end must be after start")
	}
	if end.Sub(start) > time.Duration(maxDays)*24*time.Hour {
		return ErrProfitRangeTooLarge
	}
	return nil
}

func isSubscriptionCostAccountType(accountType string) bool {
	return accountType == AccountTypeOAuth || accountType == AccountTypeSetupToken
}

func (s *ProfitStatsService) GetReport(ctx context.Context, filter ProfitStatsFilter) (*ProfitReport, error) {
	if err := validateProfitRange(filter.StartTime, filter.EndTime, profitStatsMaxRangeDays); err != nil {
		return nil, err
	}
	rows, err := s.repo.QueryProfitStats(ctx, filter)
	if err != nil {
		return nil, err
	}

	report := &ProfitReport{
		StartTime:    filter.StartTime,
		EndTime:      filter.EndTime,
		ExcludeAdmin: filter.ExcludeAdmin,
		Daily:        []ProfitDailyRow{},
		ByGroup:      []ProfitGroupRow{},
		ByAccount:    []ProfitAccountRow{},
		ByModel:      []ProfitModelRow{},
		ByUser:       []ProfitUserRow{},
	}
	accountRows := make([]ProfitStatsRawRow, 0)
	for _, row := range rows {
		switch row.Dimension {
		case ProfitDimensionTotal:
			if row.Subscription {
				report.Subscription.add(row)
			} else {
				report.Summary.add(row)
			}
		case ProfitDimensionAccount:
			accountRows = append(accountRows, row)
		}
		// 其余维度只统计按量流量，订阅号不参与利润。
		if row.Subscription {
			continue
		}
		switch row.Dimension {
		case ProfitDimensionDay:
			out := ProfitDailyRow{Date: row.Day}
			out.add(row)
			report.Daily = append(report.Daily, out)
		case ProfitDimensionGroup:
			out := ProfitGroupRow{GroupID: row.GroupID, GroupName: row.GroupName}
			out.add(row)
			report.ByGroup = append(report.ByGroup, out)
		case ProfitDimensionModel:
			out := ProfitModelRow{Model: row.Model}
			out.add(row)
			report.ByModel = append(report.ByModel, out)
		case ProfitDimensionUser:
			out := ProfitUserRow{UserID: row.UserID, Email: row.UserEmail}
			out.add(row)
			report.ByUser = append(report.ByUser, out)
		}
	}
	// 订阅号收入不计成本：总览中只呈现收入。
	report.Subscription.Cost = 0
	report.Subscription.finalize()

	byAccount, err := s.buildAccountRows(ctx, accountRows)
	if err != nil {
		return nil, err
	}
	report.ByAccount = byAccount
	for _, row := range byAccount {
		if row.CostStatus == ProfitCostStatusUnconfigured {
			report.UnconfiguredRevenue += row.Revenue
		}
	}

	sort.Slice(report.Daily, func(i, j int) bool { return report.Daily[i].Date < report.Daily[j].Date })
	sort.SliceStable(report.ByGroup, func(i, j int) bool { return report.ByGroup[i].Revenue > report.ByGroup[j].Revenue })
	sort.SliceStable(report.ByModel, func(i, j int) bool { return report.ByModel[i].Revenue > report.ByModel[j].Revenue })
	sort.SliceStable(report.ByUser, func(i, j int) bool { return report.ByUser[i].Revenue > report.ByUser[j].Revenue })

	if state, err := s.loadBackfillState(ctx); err == nil {
		report.Backfill = state
	}
	return report, nil
}

func (s *ProfitStatsService) buildAccountRows(ctx context.Context, rows []ProfitStatsRawRow) ([]ProfitAccountRow, error) {
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.AccountID > 0 {
			ids = append(ids, row.AccountID)
		}
	}
	accounts := map[int64]*Account{}
	if len(ids) > 0 && s.accountRepo != nil {
		list, err := s.accountRepo.GetByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load accounts: %w", err)
		}
		for _, account := range list {
			if account != nil {
				accounts[account.ID] = account
			}
		}
	}

	out := make([]ProfitAccountRow, 0, len(rows))
	for _, row := range rows {
		item := ProfitAccountRow{AccountID: row.AccountID, CostRate: 1}
		if account, ok := accounts[row.AccountID]; ok {
			item.AccountName = account.Name
			item.Platform = account.Platform
			item.Type = account.Type
			item.CostRate = account.BillingRateMultiplier()
			item.RateSyncEnabled = upstreamBillingRateSyncEnabled(account)
			if snapshot := decodeUpstreamBillingProbeSnapshot(account.Extra); snapshot != nil {
				item.ProbeStatus = snapshot.Status
				if rate, ok := upstreamBillingProbeSyncRate(snapshot.Data); ok {
					item.ProbeRate = &rate
				}
			}
		} else {
			item.Deleted = true
			item.AccountName = fmt.Sprintf("#%d", row.AccountID)
		}
		item.CostStatus = profitAccountCostStatus(item, row.Subscription)
		item.add(row)
		if item.CostStatus == ProfitCostStatusSubscription {
			item.Cost = 0
			item.finalize()
		}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Revenue > out[j].Revenue })
	return out, nil
}

// profitAccountCostStatus 判断账号成本倍率是否可信：倍率仍为默认 1 且没有探测声明 1 的账号，
// 视为“疑似未配置”。
func profitAccountCostStatus(row ProfitAccountRow, subscription bool) string {
	if subscription || isSubscriptionCostAccountType(row.Type) {
		return ProfitCostStatusSubscription
	}
	if row.Deleted || math.Abs(row.CostRate-1) > 1e-9 {
		return ProfitCostStatusConfigured
	}
	if row.ProbeRate != nil && math.Abs(*row.ProbeRate-1) <= 1e-9 {
		return ProfitCostStatusConfigured
	}
	return ProfitCostStatusUnconfigured
}

func (s *ProfitStatsService) loadBackfillState(ctx context.Context) (*ProfitCostBackfillState, error) {
	if s.settingRepo == nil {
		return nil, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyProfitCostBackfillState)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return nil, nil
		}
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var state ProfitCostBackfillState
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// PreviewCostRateBackfill 列出 [start, now) 内仍按默认倍率 1 记录、而账号当前成本倍率不为 1 的按量账号。
func (s *ProfitStatsService) PreviewCostRateBackfill(ctx context.Context, start time.Time) (*ProfitCostBackfillPreview, error) {
	end := s.now()
	if err := validateProfitRange(start, end, profitBackfillMaxRangeDays); err != nil {
		return nil, err
	}
	applied, err := s.loadBackfillState(ctx)
	if err != nil {
		return nil, err
	}
	candidates, err := s.repo.PreviewCostRateBackfill(ctx, start, end)
	if err != nil {
		return nil, err
	}
	if candidates == nil {
		candidates = []ProfitCostBackfillCandidate{}
	}
	return &ProfitCostBackfillPreview{StartTime: start, EndTime: end, Applied: applied, Candidates: candidates}, nil
}

// ApplyCostRateBackfill 一次性地把 [start, now) 内仍为默认倍率 1 的记录改写为账号当前成本倍率。
// 只允许执行一次：执行后写入 SettingKeyProfitCostBackfillState，之后修改倍率只影响新请求。
// accountIDs 为空时回填全部候选账号。
func (s *ProfitStatsService) ApplyCostRateBackfill(ctx context.Context, start time.Time, accountIDs []int64) (*ProfitCostBackfillState, error) {
	if !s.backfillMu.TryLock() {
		return nil, ErrProfitBackfillInProgress
	}
	defer s.backfillMu.Unlock()

	end := s.now()
	if err := validateProfitRange(start, end, profitBackfillMaxRangeDays); err != nil {
		return nil, err
	}
	applied, err := s.loadBackfillState(ctx)
	if err != nil {
		return nil, err
	}
	if applied != nil {
		return nil, ErrProfitBackfillApplied
	}

	candidates, err := s.repo.PreviewCostRateBackfill(ctx, start, end)
	if err != nil {
		return nil, err
	}
	selected := map[int64]bool{}
	for _, id := range accountIDs {
		selected[id] = true
	}

	state := &ProfitCostBackfillState{StartTime: start, EndTime: end, AccountIDs: []int64{}}
	for _, candidate := range candidates {
		if len(selected) > 0 && !selected[candidate.AccountID] {
			continue
		}
		affected, err := s.repo.ApplyCostRateBackfill(ctx, start, end, candidate.AccountID)
		if err != nil {
			// 已完成的账号保持更新；记录部分结果，避免重复执行把后来调整过的倍率再次写入历史。
			state.AppliedAt = s.now()
			_ = s.saveBackfillState(ctx, state)
			return nil, fmt.Errorf("backfill account %d: %w", candidate.AccountID, err)
		}
		state.Rows += affected
		state.AccountIDs = append(state.AccountIDs, candidate.AccountID)
	}
	state.AppliedAt = s.now()
	if err := s.saveBackfillState(ctx, state); err != nil {
		return nil, err
	}

	if s.aggregation != nil && state.Rows > 0 {
		if err := s.aggregation.TriggerRecomputeRange(start, end); err != nil {
			slog.Warn("profit_cost_backfill_recompute_failed", "error", err)
		}
	}
	slog.Info("profit_cost_backfill_applied",
		"start", start.UTC().Format(time.RFC3339),
		"end", end.UTC().Format(time.RFC3339),
		"accounts", len(state.AccountIDs),
		"rows", state.Rows,
	)
	return state, nil
}

func (s *ProfitStatsService) saveBackfillState(ctx context.Context, state *ProfitCostBackfillState) error {
	if s.settingRepo == nil {
		return errors.New("setting repository unavailable")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyProfitCostBackfillState, string(raw))
}
