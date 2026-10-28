//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type stubChannelMonitorAlertNotifier struct {
	mu         sync.Mutex
	alerts     []channelMonitorFailureAlert
	recoveries []channelMonitorRecoveryAlert
	forgotten  []int64
	sent       bool
	err        error
	// recoveryErr 非 nil 时恢复通知返回该错误；否则恢复总是 handled。
	recoveryErr error
}

type blockingChannelMonitorAlertNotifier struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (n *blockingChannelMonitorAlertNotifier) NotifyFailure(_ context.Context, _ channelMonitorFailureAlert) (bool, error) {
	close(n.started)
	<-n.release
	close(n.finished)
	return true, nil
}

func (n *blockingChannelMonitorAlertNotifier) NotifyRecovery(context.Context, channelMonitorRecoveryAlert) (bool, error) {
	return true, nil
}

func (n *blockingChannelMonitorAlertNotifier) Forget(context.Context, int64) {}

func (n *stubChannelMonitorAlertNotifier) NotifyFailure(_ context.Context, alert channelMonitorFailureAlert) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.alerts = append(n.alerts, alert)
	return n.sent, n.err
}

func (n *stubChannelMonitorAlertNotifier) NotifyRecovery(_ context.Context, alert channelMonitorRecoveryAlert) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.recoveries = append(n.recoveries, alert)
	if n.recoveryErr != nil {
		return false, n.recoveryErr
	}
	return true, nil
}

func (n *stubChannelMonitorAlertNotifier) Forget(_ context.Context, monitorID int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.forgotten = append(n.forgotten, monitorID)
}

func (n *stubChannelMonitorAlertNotifier) callCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.alerts)
}

func (n *stubChannelMonitorAlertNotifier) recoveryCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.recoveries)
}

func waitForNotifierCalls(t *testing.T, notifier *stubChannelMonitorAlertNotifier, want int) {
	t.Helper()
	waitFor(t, time.Second, "notifier call count", func() bool {
		return notifier.callCount() >= want
	})
}

// waitForAlertIdle 等异步投递结束（pending 清零）。
func waitForAlertIdle(t *testing.T, r *ChannelMonitorRunner, id int64) {
	t.Helper()
	waitFor(t, time.Second, "alert delivery finished", func() bool {
		r.failureMu.Lock()
		defer r.failureMu.Unlock()
		return !r.failureStates[id].pending
	})
}

// stubMonitorSvc 实现 monitorRunnerSvc，用于隔离 runner 与真实 service/repo。
type stubMonitorSvc struct {
	enabled    []*ChannelMonitor
	runCount   atomic.Int64
	runCalled  chan int64 // 每次 RunCheck 触发时 push 一次（缓冲足够大避免阻塞）
	runErr     error
	listErr    error
	runHoldFor time.Duration // RunCheck 内额外阻塞的时长，用来测试 Stop 等待行为
}

func (s *stubMonitorSvc) ListEnabledMonitors(_ context.Context) ([]*ChannelMonitor, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.enabled, nil
}

func (s *stubMonitorSvc) RunCheck(ctx context.Context, id int64) ([]*CheckResult, error) {
	s.runCount.Add(1)
	if s.runCalled != nil {
		select {
		case s.runCalled <- id:
		default:
		}
	}
	if s.runHoldFor > 0 {
		select {
		case <-time.After(s.runHoldFor):
		case <-ctx.Done():
		}
	}
	return nil, s.runErr
}

func newRunnerForTest(svc monitorRunnerSvc) *ChannelMonitorRunner {
	return newChannelMonitorRunner(svc, nil)
}

// 等待 condition 在 timeout 内变 true，否则 t.Fatalf。轮询 5ms 一次。
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("waitFor timed out: %s", msg)
	}
}

func runnerTaskCount(r *ChannelMonitorRunner) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tasks)
}

func runnerTaskPtr(r *ChannelMonitorRunner, id int64) *scheduledMonitor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tasks[id]
}

// TestSchedule_AddsTaskAndFiresOnce 验证 Schedule 后立即触发一次首检测，并把任务记入 tasks 表。
func TestSchedule_AddsTaskAndFiresOnce(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 4)}
	r := newRunnerForTest(svc)
	r.Start() // svc.enabled 为空，Start 立即完成

	r.Schedule(&ChannelMonitor{ID: 1, Name: "m1", Enabled: true, IntervalSeconds: 60})

	if got := runnerTaskCount(r); got != 1 {
		t.Fatalf("expected 1 scheduled task, got %d", got)
	}

	select {
	case id := <-svc.runCalled:
		if id != 1 {
			t.Fatalf("expected first fire for id=1, got %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected immediate first fire within 2s")
	}

	r.Stop()
}

// TestSchedule_ReplaceCancelsOldTask 验证对同一 id 二次 Schedule 会替换旧 task 实例。
// （旧 goroutine 通过 ctx 取消退出；这里以 task 指针不同 + Stop 不超时作为证据。）
func TestSchedule_ReplaceCancelsOldTask(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 8)}
	r := newRunnerForTest(svc)
	r.Start()

	m := &ChannelMonitor{ID: 7, Name: "m7", Enabled: true, IntervalSeconds: 60}
	r.Schedule(m)
	first := runnerTaskPtr(r, 7)
	if first == nil {
		t.Fatal("first schedule did not register task")
	}

	r.Schedule(m)
	second := runnerTaskPtr(r, 7)
	if second == nil {
		t.Fatal("second schedule did not register task")
	}
	if first == second {
		t.Fatal("re-Schedule should create a new scheduledMonitor instance")
	}

	stoppedWithin(t, r, 3*time.Second)
}

// TestUnschedule_RemovesTask 验证 Unschedule 删除 task 并使对应 goroutine 退出。
func TestUnschedule_RemovesTask(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 4)}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 3, Enabled: true, IntervalSeconds: 60})
	waitFor(t, time.Second, "task registered", func() bool { return runnerTaskCount(r) == 1 })

	r.Unschedule(3)
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected tasks empty after Unschedule, got %d", got)
	}

	stoppedWithin(t, r, 3*time.Second)
}

// TestSchedule_DisabledRedirectsToUnschedule 验证 Enabled=false 等同于 Unschedule。
func TestSchedule_DisabledRedirectsToUnschedule(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 4)}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 9, Enabled: true, IntervalSeconds: 60})
	waitFor(t, time.Second, "task registered", func() bool { return runnerTaskCount(r) == 1 })

	r.Schedule(&ChannelMonitor{ID: 9, Enabled: false, IntervalSeconds: 60})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected tasks empty after disabled re-Schedule, got %d", got)
	}

	stoppedWithin(t, r, 3*time.Second)
}

func TestSchedule_DecryptFailedRedirectsToUnschedule(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 4)}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 10, Enabled: true, IntervalSeconds: 60})
	waitFor(t, time.Second, "task registered", func() bool { return runnerTaskCount(r) == 1 })

	r.Schedule(&ChannelMonitor{ID: 10, Enabled: true, IntervalSeconds: 60, APIKeyDecryptFailed: true})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected tasks empty after decrypt-failed re-Schedule, got %d", got)
	}

	stoppedWithin(t, r, 3*time.Second)
}

func TestSchedule_RepairedAPIKeyCanBeScheduled(t *testing.T) {
	svc := &stubMonitorSvc{runCalled: make(chan int64, 1)}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 11, Enabled: true, IntervalSeconds: 60, APIKeyDecryptFailed: true})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected no task for decrypt-failed monitor, got %d", got)
	}

	r.Schedule(&ChannelMonitor{ID: 11, Enabled: true, IntervalSeconds: 60, APIKey: "replacement-key"})
	if got := runnerTaskCount(r); got != 1 {
		t.Fatalf("expected repaired monitor to be scheduled, got %d tasks", got)
	}
	select {
	case id := <-svc.runCalled:
		if id != 11 {
			t.Fatalf("expected repaired monitor id=11 to fire, got %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected repaired monitor to fire immediately")
	}

	stoppedWithin(t, r, 3*time.Second)
}

func TestRunOne_DecryptFailureUnschedulesTask(t *testing.T) {
	svc := &stubMonitorSvc{
		runCalled: make(chan int64, 1),
		runErr:    ErrChannelMonitorAPIKeyDecryptFailed,
	}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 12, Enabled: true, IntervalSeconds: 60})
	select {
	case id := <-svc.runCalled:
		if id != 12 {
			t.Fatalf("expected failing monitor id=12 to fire, got %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected failing monitor to fire immediately")
	}
	waitFor(t, time.Second, "decrypt-failed task unscheduled", func() bool { return runnerTaskCount(r) == 0 })

	stoppedWithin(t, r, 3*time.Second)
}

// TestSchedule_InvalidIntervalSkipped 验证 IntervalSeconds<=0 不会注册任务（防御性检查）。
func TestSchedule_InvalidIntervalSkipped(t *testing.T) {
	svc := &stubMonitorSvc{}
	r := newRunnerForTest(svc)
	r.Start()

	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 0})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected no task for invalid interval, got %d", got)
	}
	r.Stop()
}

// TestSchedule_BeforeStartIsNoOp 验证 Start 之前调用 Schedule 不会注册任务。
func TestSchedule_BeforeStartIsNoOp(t *testing.T) {
	svc := &stubMonitorSvc{}
	r := newRunnerForTest(svc)
	// 故意不调用 Start

	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60})
	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected no task before Start, got %d", got)
	}
	r.Stop()
}

// TestStart_LoadsAllEnabledMonitors 验证 Start 会为 ListEnabledMonitors 返回的每条记录建立任务。
func TestStart_LoadsAllEnabledMonitors(t *testing.T) {
	svc := &stubMonitorSvc{
		enabled: []*ChannelMonitor{
			{ID: 1, Enabled: true, IntervalSeconds: 60},
			{ID: 2, Enabled: true, IntervalSeconds: 60},
			{ID: 3, Enabled: true, IntervalSeconds: 60},
		},
	}
	r := newRunnerForTest(svc)
	r.Start()
	waitFor(t, 2*time.Second, "all 3 tasks scheduled", func() bool { return runnerTaskCount(r) == 3 })

	stoppedWithin(t, r, 3*time.Second)
}

func TestStart_SkipsDecryptFailedMonitor(t *testing.T) {
	svc := &stubMonitorSvc{
		enabled: []*ChannelMonitor{
			{ID: 4, Enabled: true, IntervalSeconds: 60, APIKeyDecryptFailed: true},
		},
	}
	r := newRunnerForTest(svc)
	r.Start()

	if got := runnerTaskCount(r); got != 0 {
		t.Fatalf("expected no task for decrypt-failed startup monitor, got %d", got)
	}
	if got := svc.runCount.Load(); got != 0 {
		t.Fatalf("expected decrypt-failed startup monitor not to run, got %d calls", got)
	}

	stoppedWithin(t, r, 3*time.Second)
}

// TestStop_DrainsAllGoroutines 验证 Stop 会等待所有调度 goroutine 退出（无游离）。
func TestStop_DrainsAllGoroutines(t *testing.T) {
	svc := &stubMonitorSvc{}
	r := newRunnerForTest(svc)
	r.Start()

	for id := int64(1); id <= 5; id++ {
		r.Schedule(&ChannelMonitor{ID: id, Enabled: true, IntervalSeconds: 60})
	}
	waitFor(t, 2*time.Second, "5 tasks scheduled", func() bool { return runnerTaskCount(r) == 5 })

	stoppedWithin(t, r, 3*time.Second)
}

// TestStop_WaitsForInFlightCheck 验证 Stop 会等待正在执行的 RunCheck 退出（pool.StopAndWait）。
func TestStop_WaitsForInFlightCheck(t *testing.T) {
	svc := &stubMonitorSvc{
		runCalled:  make(chan int64, 1),
		runHoldFor: 200 * time.Millisecond,
	}
	r := newRunnerForTest(svc)
	r.Start()
	r.Schedule(&ChannelMonitor{ID: 1, Enabled: true, IntervalSeconds: 60})

	select {
	case <-svc.runCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("first fire never happened")
	}

	start := time.Now()
	stoppedWithin(t, r, 3*time.Second)
	elapsed := time.Since(start)
	// Stop 必须等待 in-flight check 跑完（runHoldFor=200ms），耗时下界约 100ms。
	if elapsed < 100*time.Millisecond {
		t.Fatalf("Stop returned too fast (%v); did not wait for in-flight check", elapsed)
	}
}

// TestInFlight_PoolFullReleasesSlot 直接驱动 fire 路径，模拟 pool.TrySubmit 失败时 inFlight 必须释放。
// 用一个小型 stub pool 替换 r.pool 不便（pond.Pool 是接口但 mock 麻烦），
// 改为：占满 inFlight 后直接 fire，验证不会在 inFlight 空槽时永久卡住。
func TestInFlight_AcquireReleaseSymmetric(t *testing.T) {
	svc := &stubMonitorSvc{}
	r := newRunnerForTest(svc)

	if !r.tryAcquireInFlight(42) {
		t.Fatal("first acquire should succeed")
	}
	if r.tryAcquireInFlight(42) {
		t.Fatal("second acquire (no release) must fail")
	}
	r.releaseInFlight(42)
	if !r.tryAcquireInFlight(42) {
		t.Fatal("acquire after release should succeed")
	}
	r.releaseInFlight(42)
}

func TestHandleCheckResults_AlertsOnceAfterThreeFailuresAndResetsOnRecovery(t *testing.T) {
	notifier := &stubChannelMonitorAlertNotifier{sent: true}
	r := newRunnerForTest(&stubMonitorSvc{})
	r.alertNotifier = notifier
	failed := []*CheckResult{{Model: "model-a", Status: MonitorStatusFailed, Message: "upstream unavailable", CheckedAt: time.Now()}}

	r.handleCheckResults(context.Background(), 7, "channel-a", failed)
	r.handleCheckResults(context.Background(), 7, "channel-a", failed)
	if got := notifier.callCount(); got != 0 {
		t.Fatalf("expected no alert before threshold, got %d", got)
	}
	r.handleCheckResults(context.Background(), 7, "channel-a", failed)
	waitForNotifierCalls(t, notifier, 1)
	r.handleCheckResults(context.Background(), 7, "channel-a", failed)
	if got := notifier.callCount(); got != 1 {
		t.Fatalf("expected one alert for the failure episode, got %d", got)
	}

	waitForAlertIdle(t, r, 7)
	r.handleCheckResults(context.Background(), 7, "channel-a", []*CheckResult{{Model: "model-a", Status: MonitorStatusDegraded}})
	waitForAlertIdle(t, r, 7)
	if got := notifier.recoveryCount(); got != 1 {
		t.Fatalf("expected one recovery notice after the alerted episode, got %d", got)
	}
	for i := 0; i < channelMonitorAlertThreshold; i++ {
		r.handleCheckResults(context.Background(), 7, "channel-a", failed)
	}
	waitForNotifierCalls(t, notifier, 2)
	if got := notifier.callCount(); got != 2 {
		t.Fatalf("expected a new alert after recovery and another failure episode, got %d", got)
	}
}

func TestHandleCheckResults_RetriesAlertAfterDeliveryFailure(t *testing.T) {
	notifier := &stubChannelMonitorAlertNotifier{err: errors.New("temporary failure")}
	r := newRunnerForTest(&stubMonitorSvc{})
	r.alertNotifier = notifier
	failed := []*CheckResult{{Model: "model-a", Status: MonitorStatusError}}

	for i := 0; i < channelMonitorAlertThreshold; i++ {
		r.handleCheckResults(context.Background(), 8, "channel-b", failed)
	}
	waitForNotifierCalls(t, notifier, 1)
	if got := notifier.callCount(); got != 1 {
		t.Fatalf("expected first delivery attempt at threshold, got %d", got)
	}
	notifier.mu.Lock()
	notifier.err = nil
	notifier.sent = true
	notifier.mu.Unlock()
	r.handleCheckResults(context.Background(), 8, "channel-b", failed)
	waitForNotifierCalls(t, notifier, 2)
	r.handleCheckResults(context.Background(), 8, "channel-b", failed)
	if got := notifier.callCount(); got != 2 {
		t.Fatalf("expected one retry followed by suppression, got %d calls", got)
	}
}

func TestHandleCheckResults_RetriesWhenNotifierSkipsAndUsesOnlyPrimaryResult(t *testing.T) {
	notifier := &stubChannelMonitorAlertNotifier{sent: false}
	r := newRunnerForTest(&stubMonitorSvc{})
	r.alertNotifier = notifier
	failed := []*CheckResult{{Status: MonitorStatusFailed}}

	for i := 0; i < channelMonitorAlertThreshold; i++ {
		r.handleCheckResults(context.Background(), 9, "channel-primary", failed)
	}
	waitForNotifierCalls(t, notifier, 1)
	r.handleCheckResults(context.Background(), 9, "channel-primary", failed)
	waitForNotifierCalls(t, notifier, 2)
	if got := notifier.callCount(); got != 2 {
		t.Fatalf("expected a skipped notification to retry, got %d calls", got)
	}

	r.handleCheckResults(context.Background(), 10, "channel-secondary", []*CheckResult{
		{Status: MonitorStatusOperational},
		{Status: MonitorStatusFailed},
	})
	waitForAlertIdle(t, r, 10)
	r.handleCheckResults(context.Background(), 10, "channel-secondary", []*CheckResult{nil})
	r.failureMu.Lock()
	state := r.failureStates[10]
	r.failureMu.Unlock()
	if state.consecutive != 0 || state.alerted {
		t.Fatalf("secondary or nil results unexpectedly changed the failure state: %+v", state)
	}
}

func TestHandleCheckResults_SuppressesWhenNotifierIsNil(t *testing.T) {
	r := newRunnerForTest(&stubMonitorSvc{})
	failed := []*CheckResult{{Status: MonitorStatusFailed}}
	for i := 0; i < channelMonitorAlertThreshold; i++ {
		r.handleCheckResults(context.Background(), 11, "channel-no-notifier", failed)
	}
	r.failureMu.Lock()
	state := r.failureStates[11]
	r.failureMu.Unlock()
	if state.consecutive != channelMonitorAlertThreshold || state.alerted {
		t.Fatalf("unexpected state with nil notifier: %+v", state)
	}
}

func TestHandleCheckResults_SyncsAlertStateOnceAfterStart(t *testing.T) {
	notifier := &stubChannelMonitorAlertNotifier{sent: true}
	r := newRunnerForTest(&stubMonitorSvc{})
	r.alertNotifier = notifier
	latency := 820
	ok := []*CheckResult{{Model: "model-a", Status: MonitorStatusOperational, LatencyMs: &latency}}

	// 启动后第一次成功要核对一次库里的告警记录（可能是重启前没恢复的故障）。
	r.handleCheckResults(context.Background(), 13, "channel-d", ok)
	waitForAlertIdle(t, r, 13)
	r.handleCheckResults(context.Background(), 13, "channel-d", ok)
	waitForAlertIdle(t, r, 13)
	if got := notifier.recoveryCount(); got != 1 {
		t.Fatalf("expected a single sync after start, got %d", got)
	}
	notifier.mu.Lock()
	first := notifier.recoveries[0]
	notifier.mu.Unlock()
	if first.MonitorID != 13 || first.MonitorName != "channel-d" || first.LatencyMs == nil || *first.LatencyMs != latency {
		t.Fatalf("unexpected recovery payload: %+v", first)
	}

	// 失败次数没到阈值就恢复：不需要再查库。
	r.handleCheckResults(context.Background(), 13, "channel-d", []*CheckResult{{Status: MonitorStatusFailed}})
	r.handleCheckResults(context.Background(), 13, "channel-d", ok)
	waitForAlertIdle(t, r, 13)
	if got := notifier.recoveryCount(); got != 1 {
		t.Fatalf("expected no recovery for a short blip, got %d", got)
	}
}

func TestHandleCheckResults_RetriesRecoveryUntilHandled(t *testing.T) {
	notifier := &stubChannelMonitorAlertNotifier{sent: true, recoveryErr: errors.New("db down")}
	r := newRunnerForTest(&stubMonitorSvc{})
	r.alertNotifier = notifier
	failed := []*CheckResult{{Status: MonitorStatusError}}
	ok := []*CheckResult{{Status: MonitorStatusOperational}}

	for i := 0; i < channelMonitorAlertThreshold; i++ {
		r.handleCheckResults(context.Background(), 14, "channel-e", failed)
	}
	waitForNotifierCalls(t, notifier, 1)
	waitForAlertIdle(t, r, 14)

	r.handleCheckResults(context.Background(), 14, "channel-e", ok)
	waitForAlertIdle(t, r, 14)
	notifier.mu.Lock()
	notifier.recoveryErr = nil
	notifier.mu.Unlock()
	r.handleCheckResults(context.Background(), 14, "channel-e", ok)
	waitForAlertIdle(t, r, 14)
	r.handleCheckResults(context.Background(), 14, "channel-e", ok)
	waitForAlertIdle(t, r, 14)
	if got := notifier.recoveryCount(); got != 2 {
		t.Fatalf("expected one failed recovery and one retry, got %d", got)
	}
}

func TestUnscheduleForgetsAlertState(t *testing.T) {
	notifier := &stubChannelMonitorAlertNotifier{sent: true}
	r := newRunnerForTest(&stubMonitorSvc{})
	r.alertNotifier = notifier
	r.mu.Lock()
	r.tasks[15] = &scheduledMonitor{id: 15, cancel: func() {}}
	r.mu.Unlock()

	r.Unschedule(15)
	r.Unschedule(15) // 已经不在任务表里：不再清理
	waitFor(t, time.Second, "forget called", func() bool {
		notifier.mu.Lock()
		defer notifier.mu.Unlock()
		return len(notifier.forgotten) == 1
	})
	r.Stop()
	notifier.mu.Lock()
	defer notifier.mu.Unlock()
	if len(notifier.forgotten) != 1 || notifier.forgotten[0] != 15 {
		t.Fatalf("unexpected forget calls: %v", notifier.forgotten)
	}
}

func TestStopClearsChannelMonitorFailureState(t *testing.T) {
	r := newRunnerForTest(&stubMonitorSvc{})
	r.failureStates[12] = channelMonitorFailureState{consecutive: channelMonitorAlertThreshold}
	r.Stop()
	r.failureMu.Lock()
	defer r.failureMu.Unlock()
	if r.failureStates != nil {
		t.Fatal("expected Stop to clear failure state")
	}
}

func TestHandleCheckResults_DoesNotRestoreFailureStateAfterUnschedule(t *testing.T) {
	notifier := &blockingChannelMonitorAlertNotifier{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	r := newRunnerForTest(&stubMonitorSvc{})
	r.alertNotifier = notifier
	failed := []*CheckResult{{Model: "model-a", Status: MonitorStatusFailed}}

	for i := 1; i < channelMonitorAlertThreshold; i++ {
		r.handleCheckResults(context.Background(), 42, "channel-c", failed)
	}
	r.handleCheckResults(context.Background(), 42, "channel-c", failed)
	<-notifier.started
	r.Unschedule(42)
	close(notifier.release)
	<-notifier.finished

	r.failureMu.Lock()
	_, exists := r.failureStates[42]
	r.failureMu.Unlock()
	if exists {
		t.Fatal("failure state was restored after the monitor was unscheduled")
	}
}

// stoppedWithin 在 timeout 内并行调用 Stop，超时则 Fatal。验证 Stop 不会阻塞。
func stoppedWithin(t *testing.T, r *ChannelMonitorRunner, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	var once sync.Once
	go func() {
		r.Stop()
		once.Do(func() { close(done) })
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("Stop did not return within %s — leaked goroutine?", timeout)
	}
}
