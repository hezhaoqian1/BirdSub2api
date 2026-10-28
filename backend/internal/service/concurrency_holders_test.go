//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// holderCacheForTest 在通用 stub 之上实现 SlotHolderCache，记录写入的占用者详情。
type holderCacheForTest struct {
	stubConcurrencyCacheForTest
	accountHolders map[string]*SlotHolder
	accountLinks   map[string]string // 账号槽位 -> 回写的用户槽位
	userHolders    map[string]*SlotHolder
	userSlots      *ActiveSlotSnapshot
	accountSlots   *ActiveSlotSnapshot
	allAccountErr  error
	allAccountHits int
}

var _ SlotHolderCache = (*holderCacheForTest)(nil)

func (c *holderCacheForTest) AcquireAccountSlotWithHolder(_ context.Context, _ int64, _ int, requestID string, holder *SlotHolder, userSlotID string) (bool, error) {
	if c.accountHolders == nil {
		c.accountHolders = map[string]*SlotHolder{}
		c.accountLinks = map[string]string{}
	}
	c.accountHolders[requestID] = holder
	c.accountLinks[requestID] = userSlotID
	return c.acquireResult, c.acquireErr
}

func (c *holderCacheForTest) AcquireUserSlotWithHolder(_ context.Context, _ int64, _ int, requestID string, holder *SlotHolder) (bool, error) {
	if c.userHolders == nil {
		c.userHolders = map[string]*SlotHolder{}
	}
	c.userHolders[requestID] = holder
	return c.acquireResult, c.acquireErr
}

func (c *holderCacheForTest) ListAccountSlots(_ context.Context, _ int64) (*ActiveSlotSnapshot, error) {
	return c.accountSlots, nil
}

func (c *holderCacheForTest) ListUserSlots(_ context.Context, _ int64) (*ActiveSlotSnapshot, error) {
	return c.userSlots, nil
}

func (c *holderCacheForTest) ListAllAccountSlots(_ context.Context) (*ActiveSlotSnapshot, error) {
	c.allAccountHits++
	return c.accountSlots, c.allAccountErr
}

func requestContextForHolder() context.Context {
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req-42")
	ctx = WithSlotHolderTrace(ctx, 3, 4)
	ctx = context.WithValue(ctx, ctxkey.Group, &Group{ID: 5})
	return context.WithValue(ctx, ctxkey.Model, "claude-opus-5")
}

func onlyHolder(t *testing.T, holders map[string]*SlotHolder) (string, *SlotHolder) {
	t.Helper()
	require.Len(t, holders, 1)
	for slotID, holder := range holders {
		return slotID, holder
	}
	return "", nil
}

func TestAcquireSlotsRecordHolderFromRequestContext(t *testing.T) {
	cache := &holderCacheForTest{stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{acquireResult: true}}
	svc := NewConcurrencyService(cache)
	ctx := requestContextForHolder()

	result, err := svc.AcquireAccountSlot(ctx, 7, 2)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	accountSlotID, holder := onlyHolder(t, cache.accountHolders)
	require.Equal(t, SlotHolder{RequestID: "req-42", UserID: 3, APIKeyID: 4, GroupID: 5, AccountID: 7, Model: "claude-opus-5"}, *holder)
	require.Empty(t, cache.accountLinks[accountSlotID], "no user slot held yet")

	// 用户槽位以显式传入的 userID 为准。
	_, err = svc.AcquireUserSlot(ctx, 99, 2)
	require.NoError(t, err)
	_, holder = onlyHolder(t, cache.userHolders)
	require.Equal(t, int64(99), holder.UserID)
	require.Zero(t, holder.AccountID)
}

func TestAccountSlotLinksBackToUserSlotOfSameRequest(t *testing.T) {
	cache := &holderCacheForTest{stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{acquireResult: true}}
	svc := NewConcurrencyService(cache)
	ctx := requestContextForHolder()

	_, err := svc.AcquireUserSlot(ctx, 3, 2)
	require.NoError(t, err)
	userSlotID, _ := onlyHolder(t, cache.userHolders)

	_, err = svc.AcquireAccountSlot(ctx, 7, 2)
	require.NoError(t, err)
	accountSlotID, _ := onlyHolder(t, cache.accountHolders)
	require.Equal(t, userSlotID, cache.accountLinks[accountSlotID])

	// 另一个请求（另一个 trace）不会回写到这个用户槽位。
	_, err = svc.AcquireAccountSlot(requestContextForHolder(), 8, 2)
	require.NoError(t, err)
	require.Len(t, cache.accountLinks, 2)
	for slotID, link := range cache.accountLinks {
		if slotID != accountSlotID {
			require.Empty(t, link)
		}
	}
}

func TestFailedUserSlotIsNotLinked(t *testing.T) {
	cache := &holderCacheForTest{stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{acquireResult: false}}
	svc := NewConcurrencyService(cache)
	ctx := requestContextForHolder()

	_, err := svc.AcquireUserSlot(ctx, 3, 2)
	require.NoError(t, err)
	_, userSlotID := slotHolderFromContext(ctx, 0)
	require.Empty(t, userSlotID)
}

func TestFallbackGroupOverridesAuthGroupInHolder(t *testing.T) {
	cache := &holderCacheForTest{stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{acquireResult: true}}
	svc := NewConcurrencyService(cache)
	ctx := requestContextForHolder()
	SlotHolderTraceFromContext(ctx).SetGroupID(88)

	_, err := svc.AcquireAccountSlot(ctx, 7, 2)
	require.NoError(t, err)
	_, holder := onlyHolder(t, cache.accountHolders)
	require.Equal(t, int64(88), holder.GroupID)
}

func TestHolderFallsBackToUserIDContextWithoutTrace(t *testing.T) {
	cache := &holderCacheForTest{stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{acquireResult: true}}
	svc := NewConcurrencyService(cache)
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(6))

	_, err := svc.AcquireAccountSlot(ctx, 7, 2)
	require.NoError(t, err)
	_, holder := onlyHolder(t, cache.accountHolders)
	require.Equal(t, int64(6), holder.UserID)
	// 没有 trace 时 nil 接收者上的方法也安全。
	SlotHolderTraceFromContext(ctx).SetGroupID(1)
}

func TestAcquireSlotWithoutIdentifyingContextWritesNoHolder(t *testing.T) {
	cache := &holderCacheForTest{stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{acquireResult: true}}
	svc := NewConcurrencyService(cache)

	_, err := svc.AcquireAccountSlot(context.Background(), 7, 2)
	require.NoError(t, err)
	_, holder := onlyHolder(t, cache.accountHolders)
	require.Nil(t, holder)
}

func TestSlotHolderDetailsCanBeDisabled(t *testing.T) {
	cache := &holderCacheForTest{stubConcurrencyCacheForTest: stubConcurrencyCacheForTest{acquireResult: true}}
	svc := NewConcurrencyService(cache)
	svc.SetSlotHolderDetailsEnabled(false)

	result, err := svc.AcquireAccountSlot(requestContextForHolder(), 7, 2)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Empty(t, cache.accountHolders, "disabled details must use the plain acquire path")

	svc.SetSlotHolderDetailsEnabled(true)
	_, err = svc.AcquireAccountSlot(requestContextForHolder(), 7, 2)
	require.NoError(t, err)
	require.Len(t, cache.accountHolders, 1)
}

func TestListUserSlotHoldersSkipsAccountScanForLimitedUser(t *testing.T) {
	cache := &holderCacheForTest{
		userSlots: &ActiveSlotSnapshot{Now: 100, Slots: []ActiveSlot{
			{SlotID: "u1", AccountID: 11, StartedAt: 50, Holder: &SlotHolder{UserID: 3, AccountID: 11}},
		}},
		accountSlots: &ActiveSlotSnapshot{Now: 100, Slots: []ActiveSlot{
			{SlotID: "a9", AccountID: 12, StartedAt: 70, Holder: &SlotHolder{UserID: 3}},
		}},
	}
	svc := NewConcurrencyService(cache)

	snapshot, err := svc.ListUserSlotHolders(context.Background(), 3, false)
	require.NoError(t, err)
	require.Len(t, snapshot.Slots, 1)
	require.Equal(t, int64(11), snapshot.Slots[0].AccountID)
	require.Zero(t, cache.allAccountHits, "limited users hold a user slot for every request; no global scan")
}

func TestListUserSlotHoldersFindsUnlimitedUserRequestsInAccountSlots(t *testing.T) {
	cache := &holderCacheForTest{
		userSlots: &ActiveSlotSnapshot{Now: 100, Slots: []ActiveSlot{
			{SlotID: "live-u", Live: true, StartedAt: 99},
		}},
		accountSlots: &ActiveSlotSnapshot{Now: 100, Slots: []ActiveSlot{
			{SlotID: "a9", AccountID: 12, StartedAt: 70, Holder: &SlotHolder{UserID: 3}},
			{SlotID: "a8", AccountID: 12, StartedAt: 60, Holder: &SlotHolder{UserID: 4}},
			{SlotID: "a7", AccountID: 13, StartedAt: 40, Holder: &SlotHolder{UserID: 3}},
			{SlotID: "a6", AccountID: 13, StartedAt: 30},
		}},
	}
	svc := NewConcurrencyService(cache)

	snapshot, err := svc.ListUserSlotHolders(context.Background(), 3, true)
	require.NoError(t, err)
	got := make([]string, 0, len(snapshot.Slots))
	for _, slot := range snapshot.Slots {
		got = append(got, slot.SlotID)
	}
	require.Equal(t, []string{"a7", "a9", "live-u"}, got, "other users excluded, live leases last")
}

func TestListUserSlotHoldersSurvivesAccountScanFailure(t *testing.T) {
	cache := &holderCacheForTest{
		userSlots:     &ActiveSlotSnapshot{Now: 100, Slots: []ActiveSlot{{SlotID: "u1", Holder: &SlotHolder{RequestID: "r1"}}}},
		allAccountErr: context.DeadlineExceeded,
	}
	svc := NewConcurrencyService(cache)

	snapshot, err := svc.ListUserSlotHolders(context.Background(), 3, true)
	require.NoError(t, err)
	require.Len(t, snapshot.Slots, 1)
}

func TestListSlotHoldersWithoutHolderCacheReturnsEmpty(t *testing.T) {
	svc := NewConcurrencyService(&stubConcurrencyCacheForTest{})

	snapshot, err := svc.ListAccountSlotHolders(context.Background(), 1)
	require.NoError(t, err)
	require.Empty(t, snapshot.Slots)
	snapshot, err = svc.ListUserSlotHolders(context.Background(), 1, true)
	require.NoError(t, err)
	require.Empty(t, snapshot.Slots)
}
