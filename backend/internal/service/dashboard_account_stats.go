package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

func (s *DashboardService) GetAccountStatsWithUsageFilters(ctx context.Context, startTime, endTime time.Time, filters usagestats.UsageLogFilters) ([]usagestats.AccountStat, error) {
	repo, ok := s.usageRepo.(interface {
		GetAccountStatsWithUsageFilters(context.Context, time.Time, time.Time, usagestats.UsageLogFilters) ([]usagestats.AccountStat, error)
	})
	if !ok {
		return nil, fmt.Errorf("account usage statistics are not supported by the repository")
	}
	stats, err := repo.GetAccountStatsWithUsageFilters(ctx, startTime, endTime, filters)
	if err != nil {
		return nil, fmt.Errorf("get account usage statistics: %w", err)
	}
	return stats, nil
}
