package service

import (
	"context"
	"sort"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// SlotHolder 描述占用某个并发槽位的请求，供管理后台查看"谁在用"。
// 每个在途槽位都会在 Redis 存一份，所以 JSON 字段名刻意缩短。
type SlotHolder struct {
	// RequestID 仅用于展示（便于对照日志），可能来自客户端 X-Request-ID，不能当作关联键。
	RequestID string `json:"rid,omitempty"`
	UserID    int64  `json:"uid,omitempty"`
	APIKeyID  int64  `json:"kid,omitempty"`
	GroupID   int64  `json:"gid,omitempty"`
	// AccountID 为请求所在账号：账号槽位即该账号；用户槽位在请求占到账号槽位时回写。
	AccountID int64  `json:"aid,omitempty"`
	Model     string `json:"m,omitempty"`
}

// SlotHolderTrace 由认证中间件放入请求 context：带上占用者身份，并记下本请求占到的用户槽位，
// 之后占账号槽位时把所在账号回写进该用户槽位的详情。只服务于"谁在用"展示，不参与任何策略判断。
type SlotHolderTrace struct {
	UserID   int64
	APIKeyID int64

	mu         sync.Mutex
	groupID    int64 // 非 0 时覆盖认证分组（兜底分组重试）
	userSlotID string
}

// WithSlotHolderTrace 返回带占用者身份的 context。
func WithSlotHolderTrace(ctx context.Context, userID, apiKeyID int64) context.Context {
	return context.WithValue(ctx, ctxkey.SlotHolderTrace, &SlotHolderTrace{UserID: userID, APIKeyID: apiKeyID})
}

// SlotHolderTraceFromContext 取出请求的占用者身份，没有时返回 nil（方法均可在 nil 上调用）。
func SlotHolderTraceFromContext(ctx context.Context) *SlotHolderTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(ctxkey.SlotHolderTrace).(*SlotHolderTrace)
	return trace
}

// SetGroupID 记录请求改走的分组（如兜底分组重试），之后占的槽位按该分组展示。
func (t *SlotHolderTrace) SetGroupID(groupID int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.groupID = groupID
	t.mu.Unlock()
}

func (t *SlotHolderTrace) snapshot() (groupID int64, userSlotID string) {
	if t == nil {
		return 0, ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.groupID, t.userSlotID
}

func (t *SlotHolderTrace) setUserSlot(slotID string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.userSlotID = slotID
	t.mu.Unlock()
}

// ActiveSlot 是 Redis 中一个仍然有效的槽位成员。
type ActiveSlot struct {
	SlotID string
	// Live 表示 OpenAI Live 会话租约，这类租约不记录占用者详情。
	Live bool
	// AccountID 对账号槽位取自槽位键；对用户槽位取自详情回写，0 表示还没占到账号槽位。
	AccountID int64
	// StartedAt 为占槽时的 Redis Unix 秒；Live 租约会定期续租，这里只是最近一次续租时间。
	StartedAt int64
	// Holder 为 nil 表示没有详情（功能上线前的请求、详情记录已关闭、写入失败或已过期）。
	Holder *SlotHolder
}

// ActiveSlotSnapshot 是某一时刻的槽位快照，Now 为 Redis 服务器时间（Unix 秒）。
type ActiveSlotSnapshot struct {
	Now   int64
	Slots []ActiveSlot
}

// SlotHolderCache 由 Redis 并发缓存可选实现：占槽成功时顺带写入占用者详情，并可按槽位读回。
// 详情写入与活跃索引走同一个 pipeline，释放时与 ZREM 同一个 pipeline 删除，不增加热路径往返。
// holder 只在占槽成功后才序列化，排队轮询失败不产生额外开销。
type SlotHolderCache interface {
	// AcquireAccountSlotWithHolder 占账号槽位；userSlotID 非空时把详情（含所在账号）同时回写到该用户槽位。
	AcquireAccountSlotWithHolder(ctx context.Context, accountID int64, maxConcurrency int, requestID string, holder *SlotHolder, userSlotID string) (bool, error)
	AcquireUserSlotWithHolder(ctx context.Context, userID int64, maxConcurrency int, requestID string, holder *SlotHolder) (bool, error)
	ListAccountSlots(ctx context.Context, accountID int64) (*ActiveSlotSnapshot, error)
	ListUserSlots(ctx context.Context, userID int64) (*ActiveSlotSnapshot, error)
	// ListAllAccountSlots 通过账号活跃索引读取全部账号的在途槽位。
	ListAllAccountSlots(ctx context.Context) (*ActiveSlotSnapshot, error)
}

// SetSlotHolderDetailsEnabled 开关占用者详情记录。关闭后占槽不再写详情，
// 已写入的详情随槽位释放或 TTL 自然清除。
func (s *ConcurrencyService) SetSlotHolderDetailsEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.slotHolderDetailsOff.Store(!enabled)
}

func (s *ConcurrencyService) slotHolderWriter() (SlotHolderCache, bool) {
	if s == nil || s.cache == nil || s.slotHolderDetailsOff.Load() {
		return nil, false
	}
	hc, ok := s.cache.(SlotHolderCache)
	return hc, ok
}

func (s *ConcurrencyService) acquireAccountSlotInCache(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	if hc, ok := s.slotHolderWriter(); ok {
		holder, userSlotID := slotHolderFromContext(ctx, 0)
		if holder != nil {
			holder.AccountID = accountID
		}
		return hc.AcquireAccountSlotWithHolder(ctx, accountID, maxConcurrency, requestID, holder, userSlotID)
	}
	return s.cache.AcquireAccountSlot(ctx, accountID, maxConcurrency, requestID)
}

func (s *ConcurrencyService) acquireUserSlotInCache(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	if hc, ok := s.slotHolderWriter(); ok {
		holder, _ := slotHolderFromContext(ctx, userID)
		acquired, err := hc.AcquireUserSlotWithHolder(ctx, userID, maxConcurrency, requestID, holder)
		if acquired && holder != nil {
			SlotHolderTraceFromContext(ctx).setUserSlot(requestID)
		}
		return acquired, err
	}
	return s.cache.AcquireUserSlot(ctx, userID, maxConcurrency, requestID)
}

// slotHolderFromContext 从请求 context 提取占用者详情（认证中间件与 ops 上下文已写入），
// 同时返回本请求已占的用户槽位 ID。没有任何可识别信息时返回 nil，此时只占槽不写详情。
func slotHolderFromContext(ctx context.Context, userID int64) (*SlotHolder, string) {
	if ctx == nil {
		return nil, ""
	}
	trace := SlotHolderTraceFromContext(ctx)
	groupID, userSlotID := trace.snapshot()
	holder := &SlotHolder{UserID: userID, GroupID: groupID}
	if trace != nil {
		if holder.UserID <= 0 {
			holder.UserID = trace.UserID
		}
		holder.APIKeyID = trace.APIKeyID
	}
	if holder.UserID <= 0 {
		holder.UserID, _ = ctx.Value(ctxkey.UserID).(int64)
	}
	if holder.GroupID <= 0 {
		if group, ok := ctx.Value(ctxkey.Group).(*Group); ok && group != nil {
			holder.GroupID = group.ID
		}
	}
	holder.RequestID, _ = ctx.Value(ctxkey.RequestID).(string)
	holder.Model, _ = ctx.Value(ctxkey.Model).(string)
	if holder.RequestID == "" && holder.UserID <= 0 && holder.APIKeyID <= 0 && holder.Model == "" {
		return nil, ""
	}
	return holder, userSlotID
}

func (s *ConcurrencyService) slotHolderReader() (SlotHolderCache, bool) {
	if s == nil || s.cache == nil {
		return nil, false
	}
	hc, ok := s.cache.(SlotHolderCache)
	return hc, ok
}

// ListAccountSlotHolders 返回账号当前占用的全部槽位（含 Live 租约），按开始时间升序。
func (s *ConcurrencyService) ListAccountSlotHolders(ctx context.Context, accountID int64) (*ActiveSlotSnapshot, error) {
	hc, ok := s.slotHolderReader()
	if !ok {
		return &ActiveSlotSnapshot{}, nil
	}
	return hc.ListAccountSlots(ctx, accountID)
}

// ListUserSlotHolders 返回用户当前占用的槽位，所在账号来自账号占槽时回写的详情。
// 用户并发不限（unlimited）时不占用户槽位，这类请求只出现在账号槽位里，
// 只能扫一遍全部活跃账号按 UserID 找回来；有并发上限的用户不需要这次扫描。
func (s *ConcurrencyService) ListUserSlotHolders(ctx context.Context, userID int64, unlimited bool) (*ActiveSlotSnapshot, error) {
	hc, ok := s.slotHolderReader()
	if !ok {
		return &ActiveSlotSnapshot{}, nil
	}
	snapshot, err := hc.ListUserSlots(ctx, userID)
	if err != nil || !unlimited {
		return snapshot, err
	}
	accountSnapshot, err := hc.ListAllAccountSlots(ctx)
	if err != nil {
		// 账号槽位只是补充信息，失败时仍返回用户槽位。
		logger.L().Warn("concurrency.list_all_account_slots_failed", zap.Int64("user_id", userID), zap.Error(err))
		return snapshot, nil
	}
	appendUserAccountSlots(snapshot, accountSnapshot, userID)
	return snapshot, nil
}

// appendUserAccountSlots 把属于该用户、但不占用户槽位的账号槽位并入快照。
func appendUserAccountSlots(snapshot, accountSnapshot *ActiveSlotSnapshot, userID int64) {
	if snapshot == nil || accountSnapshot == nil {
		return
	}
	for _, slot := range accountSnapshot.Slots {
		if slot.Holder != nil && slot.Holder.UserID == userID {
			snapshot.Slots = append(snapshot.Slots, slot)
		}
	}
	SortActiveSlots(snapshot.Slots)
}

// SortActiveSlots 普通槽位在前、按开始时间升序（运行最久的在前）；Live 租约的分数会随续租变化，排在最后。
func SortActiveSlots(slots []ActiveSlot) {
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].Live != slots[j].Live {
			return !slots[i].Live
		}
		if slots[i].StartedAt != slots[j].StartedAt {
			return slots[i].StartedAt < slots[j].StartedAt
		}
		return slots[i].SlotID < slots[j].SlotID
	})
}
