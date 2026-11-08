//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestHandleCheckResults_AlertCarriesRecentFailures(t *testing.T) {
	notifier := &stubChannelMonitorAlertNotifier{sent: false}
	r := newRunnerForTest(&stubMonitorSvc{})
	r.alertNotifier = notifier
	base := time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC)
	fail := func(i int) []*CheckResult {
		return []*CheckResult{{Model: "m", Status: MonitorStatusError, Message: fmt.Sprintf("err-%d", i), CheckedAt: base.Add(time.Duration(i) * time.Minute)}}
	}

	for i := 1; i <= channelMonitorAlertThreshold; i++ {
		r.handleCheckResults(context.Background(), 5, "c", fail(i))
	}
	waitForNotifierCalls(t, notifier, 1)
	waitForAlertIdle(t, r, 5)
	// 通知没落定（sent=false），下一次失败会重试：只带最近 3 次。
	r.handleCheckResults(context.Background(), 5, "c", fail(4))
	waitForNotifierCalls(t, notifier, 2)
	waitForAlertIdle(t, r, 5)

	notifier.mu.Lock()
	first, second := notifier.alerts[0], notifier.alerts[1]
	notifier.mu.Unlock()
	if got := len(first.Failures); got != 3 || first.Failures[0].Message != "err-1" || first.Failures[2].Message != "err-3" {
		t.Fatalf("first alert failures = %+v", first.Failures)
	}
	if got := len(second.Failures); got != 3 || second.Failures[0].Message != "err-2" || second.Failures[2].Message != "err-4" {
		t.Fatalf("second alert failures = %+v", second.Failures)
	}
	if !second.Failures[0].CheckedAt.Equal(base.Add(2 * time.Minute)) {
		t.Fatalf("expected sample check time kept, got %s", second.Failures[0].CheckedAt)
	}

	r.handleCheckResults(context.Background(), 5, "c", []*CheckResult{{Model: "m", Status: MonitorStatusOperational}})
	waitForAlertIdle(t, r, 5)
	r.failureMu.Lock()
	recent := r.failureStates[5].recent
	r.failureMu.Unlock()
	if len(recent) != 0 {
		t.Fatalf("expected failures cleared after success, got %+v", recent)
	}
}
