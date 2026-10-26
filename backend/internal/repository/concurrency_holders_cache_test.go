package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newHolderTestCache(t *testing.T) (*miniredis.Miniredis, *concurrencyCache) {
	t.Helper()
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return redisServer, NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
}

func TestSlotHolderWrittenOnAcquireAndDeletedOnRelease(t *testing.T) {
	redisServer, cache := newHolderTestCache(t)
	ctx := context.Background()

	acquired, err := cache.AcquireAccountSlotWithHolder(ctx, 7, 2, "slot-a", &service.SlotHolder{RequestID: "req-1", UserID: 3, APIKeyID: 4, GroupID: 5, AccountID: 7, Model: "claude-opus"}, "")
	require.NoError(t, err)
	require.True(t, acquired)
	require.True(t, redisServer.Exists(slotHolderKey("slot-a")))
	ttl := redisServer.TTL(slotHolderKey("slot-a"))
	require.Equal(t, 15*time.Minute, ttl, "holder TTL should match slot TTL")

	snapshot, err := cache.ListAccountSlots(ctx, 7)
	require.NoError(t, err)
	require.Len(t, snapshot.Slots, 1)
	slot := snapshot.Slots[0]
	require.Equal(t, "slot-a", slot.SlotID)
	require.Equal(t, int64(7), slot.AccountID)
	require.False(t, slot.Live)
	require.NotNil(t, slot.Holder)
	require.Equal(t, service.SlotHolder{RequestID: "req-1", UserID: 3, APIKeyID: 4, GroupID: 5, AccountID: 7, Model: "claude-opus"}, *slot.Holder)

	require.NoError(t, cache.ReleaseAccountSlot(ctx, 7, "slot-a"))
	require.False(t, redisServer.Exists(slotHolderKey("slot-a")))
	snapshot, err = cache.ListAccountSlots(ctx, 7)
	require.NoError(t, err)
	require.Empty(t, snapshot.Slots)
}

func TestSlotHolderNotWrittenWhenAcquireFails(t *testing.T) {
	redisServer, cache := newHolderTestCache(t)
	ctx := context.Background()

	acquired, err := cache.AcquireUserSlotWithHolder(ctx, 9, 1, "slot-1", &service.SlotHolder{RequestID: "r1"})
	require.NoError(t, err)
	require.True(t, acquired)
	acquired, err = cache.AcquireUserSlotWithHolder(ctx, 9, 1, "slot-2", &service.SlotHolder{RequestID: "r2"})
	require.NoError(t, err)
	require.False(t, acquired)
	require.False(t, redisServer.Exists(slotHolderKey("slot-2")))
}

func TestPlainAcquireStillWorksWithoutHolder(t *testing.T) {
	redisServer, cache := newHolderTestCache(t)
	ctx := context.Background()

	acquired, err := cache.AcquireUserSlot(ctx, 9, 2, "plain")
	require.NoError(t, err)
	require.True(t, acquired)
	require.False(t, redisServer.Exists(slotHolderKey("plain")))

	snapshot, err := cache.ListUserSlots(ctx, 9)
	require.NoError(t, err)
	require.Len(t, snapshot.Slots, 1)
	require.Nil(t, snapshot.Slots[0].Holder, "slots without details are listed as unrecorded")
	require.Zero(t, snapshot.Slots[0].AccountID)

	count, err := cache.GetUserConcurrency(ctx, 9)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestListAllAccountSlotsUsesActiveIndexAndIncludesLiveLeases(t *testing.T) {
	_, cache := newHolderTestCache(t)
	ctx := context.Background()

	for _, tc := range []struct {
		accountID int64
		slotID    string
		holder    service.SlotHolder
	}{
		{1, "a1", service.SlotHolder{RequestID: "r1", UserID: 10}},
		{1, "a2", service.SlotHolder{RequestID: "r2", UserID: 11}},
		{2, "b1", service.SlotHolder{RequestID: "r3", UserID: 10}},
	} {
		acquired, err := cache.AcquireAccountSlotWithHolder(ctx, tc.accountID, 5, tc.slotID, &tc.holder, "")
		require.NoError(t, err)
		require.True(t, acquired)
	}
	acquired, err := cache.AcquireLiveLease(ctx, 1, 5, 10, 5, 30, "live-1", false)
	require.NoError(t, err)
	require.True(t, acquired)

	snapshot, err := cache.ListAllAccountSlots(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Slots, 4)
	byID := map[string]service.ActiveSlot{}
	for _, slot := range snapshot.Slots {
		byID[slot.SlotID] = slot
	}
	require.Equal(t, int64(2), byID["b1"].AccountID)
	require.Equal(t, "r3", byID["b1"].Holder.RequestID)
	require.True(t, byID["live-1"].Live)
	require.Nil(t, byID["live-1"].Holder)

	require.NoError(t, cache.ReleaseAccountSlot(ctx, 2, "b1"))
	snapshot, err = cache.ListAllAccountSlots(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Slots, 3)
}

func TestListSlotsSkipsExpiredMembers(t *testing.T) {
	redisServer, cache := newHolderTestCache(t)
	ctx := context.Background()

	acquired, err := cache.AcquireAccountSlotWithHolder(ctx, 3, 5, "old", &service.SlotHolder{RequestID: "old"}, "")
	require.NoError(t, err)
	require.True(t, acquired)
	// 槽位分数用 Redis TIME；把服务器时间推过 TTL 后，该成员应视为过期。
	redisServer.SetTime(time.Now().Add(16 * time.Minute))

	snapshot, err := cache.ListAccountSlots(ctx, 3)
	require.NoError(t, err)
	require.Empty(t, snapshot.Slots)
}

func TestAccountSlotWritesAccountBackToUserSlot(t *testing.T) {
	_, cache := newHolderTestCache(t)
	ctx := context.Background()

	acquired, err := cache.AcquireUserSlotWithHolder(ctx, 9, 3, "user-slot", &service.SlotHolder{RequestID: "r1", UserID: 9, GroupID: 5})
	require.NoError(t, err)
	require.True(t, acquired)
	snapshot, err := cache.ListUserSlots(ctx, 9)
	require.NoError(t, err)
	require.Len(t, snapshot.Slots, 1)
	require.Zero(t, snapshot.Slots[0].AccountID, "no account slot yet")

	acquired, err = cache.AcquireAccountSlotWithHolder(ctx, 42, 3, "account-slot", &service.SlotHolder{RequestID: "r1", UserID: 9, GroupID: 5, AccountID: 42}, "user-slot")
	require.NoError(t, err)
	require.True(t, acquired)
	snapshot, err = cache.ListUserSlots(ctx, 9)
	require.NoError(t, err)
	require.Len(t, snapshot.Slots, 1)
	require.Equal(t, int64(42), snapshot.Slots[0].AccountID)
	require.Equal(t, int64(5), snapshot.Slots[0].Holder.GroupID)
}

func TestAccountSlotDoesNotRecreateReleasedUserSlotHolder(t *testing.T) {
	redisServer, cache := newHolderTestCache(t)
	ctx := context.Background()

	acquired, err := cache.AcquireUserSlotWithHolder(ctx, 9, 3, "user-slot", &service.SlotHolder{UserID: 9})
	require.NoError(t, err)
	require.True(t, acquired)
	require.NoError(t, cache.ReleaseUserSlot(ctx, 9, "user-slot"))

	acquired, err = cache.AcquireAccountSlotWithHolder(ctx, 42, 3, "account-slot", &service.SlotHolder{UserID: 9, AccountID: 42}, "user-slot")
	require.NoError(t, err)
	require.True(t, acquired)
	require.True(t, redisServer.Exists(slotHolderKey("account-slot")))
	require.False(t, redisServer.Exists(slotHolderKey("user-slot")), "released user slot must not get an orphan holder key")
}
