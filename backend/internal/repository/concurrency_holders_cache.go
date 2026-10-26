package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// 槽位占用者详情：每个在途槽位一个字符串键，值为 service.SlotHolder 的 JSON。
// 占槽成功时写入、释放时删除，TTL 与槽位一致，进程崩溃遗留的详情会自然过期。
// 格式: concurrency:holder:{slotID}
const slotHolderKeyPrefix = "concurrency:holder:"

// slotListChunkSize 限制单个读 pipeline 的命令数，避免一次占用 Redis 太久。
const slotListChunkSize = 500

var _ service.SlotHolderCache = (*concurrencyCache)(nil)

func slotHolderKey(slotID string) string {
	return slotHolderKeyPrefix + slotID
}

// markSlotAcquired 是占槽成功后的 best-effort 标记：活跃索引 + 占用者详情走同一 pipeline，一次往返。
// linkedSlotID 非空时把同一份详情回写到该槽位（用户槽位），只在其详情仍存在时覆盖（SET XX），
// 不会给已释放的槽位留下孤儿键。失败只记日志，不影响槽位本身。
func (c *concurrencyCache) markSlotAcquired(ctx context.Context, indexKey string, id int64, expireAt int64, slotID string, holder *service.SlotHolder, linkedSlotID string) {
	var payload []byte
	if holder != nil && slotID != "" {
		payload, _ = json.Marshal(holder)
	}
	if len(payload) == 0 {
		c.touchActiveIndexAt(ctx, indexKey, id, expireAt)
		return
	}
	if c == nil || c.rdb == nil || id <= 0 || expireAt <= 0 {
		return
	}
	ttl := time.Duration(c.slotTTLSeconds) * time.Second
	pipe := c.rdb.Pipeline()
	pipe.ZAdd(ctx, indexKey, redis.Z{
		Score:  float64(expireAt),
		Member: strconv.FormatInt(id, 10),
	})
	pipe.Set(ctx, slotHolderKey(slotID), payload, ttl)
	if linkedSlotID != "" && linkedSlotID != slotID {
		pipe.SetXX(ctx, slotHolderKey(linkedSlotID), payload, ttl)
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		logger.LegacyPrintf("repository.concurrency", "Warning: mark slot %s acquired on %s for %d failed: %v", slotID, indexKey, id, err)
	}
}

// removeSlotMember 移除槽位成员并顺带删除占用者详情（同一 pipeline，一次往返）。
// 只有 ZREM 的错误会上抛：详情键即使残留也会随 TTL 过期，且读取时只认仍在槽位里的成员。
func (c *concurrencyCache) removeSlotMember(ctx context.Context, slotKey string, slotID string) error {
	pipe := c.rdb.Pipeline()
	zrem := pipe.ZRem(ctx, slotKey, slotID)
	pipe.Del(ctx, slotHolderKey(slotID))
	_, _ = pipe.Exec(ctx)
	return zrem.Err()
}

// slotListTarget 描述一组需要读取的槽位键。
type slotListTarget struct {
	regularKey string
	liveKey    string
	accountID  int64 // 账号槽位填账号 ID，用户槽位为 0
}

func (c *concurrencyCache) ListAccountSlots(ctx context.Context, accountID int64) (*service.ActiveSlotSnapshot, error) {
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		return nil, err
	}
	return c.listSlotsAt(ctx, now, []slotListTarget{{
		regularKey: accountSlotKey(accountID),
		liveKey:    liveAccountSlotKey(accountID),
		accountID:  accountID,
	}})
}

func (c *concurrencyCache) ListUserSlots(ctx context.Context, userID int64) (*service.ActiveSlotSnapshot, error) {
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		return nil, err
	}
	return c.listSlotsAt(ctx, now, []slotListTarget{{
		regularKey: userSlotKey(userID),
		liveKey:    liveUserSlotKey(userID),
	}})
}

// ListAllAccountSlots 依赖 best-effort 的账号活跃索引定位账号，索引偶尔漏掉的账号要等它下次占槽/释放才会重新出现。
// 只在查看不限并发用户时使用（这类用户不占用户槽位，没有更直接的入口）。
func (c *concurrencyCache) ListAllAccountSlots(ctx context.Context) (*service.ActiveSlotSnapshot, error) {
	now, err := c.redisUnixSeconds(ctx)
	if err != nil {
		return nil, err
	}
	// 活跃索引的 score 是“预计仍需关注到”的时间，未到期的成员才可能还有槽位。
	members, err := c.rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     accountActiveIndexKey,
		Start:   strconv.FormatInt(now, 10),
		Stop:    "+inf",
		ByScore: true,
	}).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("read account active index: %w", err)
	}
	targets := make([]slotListTarget, 0, len(members))
	for _, member := range members {
		accountID, parseErr := strconv.ParseInt(member, 10, 64)
		if parseErr != nil || accountID <= 0 {
			continue
		}
		targets = append(targets, slotListTarget{
			regularKey: accountSlotKey(accountID),
			liveKey:    liveAccountSlotKey(accountID),
			accountID:  accountID,
		})
	}
	return c.listSlotsAt(ctx, now, targets)
}

// listSlotsAt 读取各目标中仍有效的槽位成员（与计数脚本的过期口径一致），再批量取回占用者详情。
func (c *concurrencyCache) listSlotsAt(ctx context.Context, now int64, targets []slotListTarget) (*service.ActiveSlotSnapshot, error) {
	snapshot := &service.ActiveSlotSnapshot{Now: now}
	if len(targets) == 0 {
		return snapshot, nil
	}
	// 计数脚本用 ZREMRANGEBYSCORE -inf cutoff 清理（含 cutoff），所以有效成员是 score > cutoff。
	regularMin := "(" + strconv.FormatInt(now-int64(c.slotTTLSeconds), 10)
	liveMin := "(" + strconv.FormatInt(now-liveLeaseTTLSeconds, 10)

	type targetCmds struct {
		target  slotListTarget
		regular *redis.ZSliceCmd
		live    *redis.ZSliceCmd
	}
	for start := 0; start < len(targets); start += slotListChunkSize / 2 {
		end := min(start+slotListChunkSize/2, len(targets))
		pipe := c.rdb.Pipeline()
		cmds := make([]targetCmds, 0, end-start)
		for _, target := range targets[start:end] {
			cmds = append(cmds, targetCmds{
				target:  target,
				regular: pipe.ZRangeByScoreWithScores(ctx, target.regularKey, &redis.ZRangeBy{Min: regularMin, Max: "+inf"}),
				live:    pipe.ZRangeByScoreWithScores(ctx, target.liveKey, &redis.ZRangeBy{Min: liveMin, Max: "+inf"}),
			})
		}
		if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
			return nil, fmt.Errorf("read slot members: %w", err)
		}
		for _, cmd := range cmds {
			for _, z := range cmd.regular.Val() {
				snapshot.Slots = append(snapshot.Slots, activeSlotFromZ(z, cmd.target.accountID, false))
			}
			for _, z := range cmd.live.Val() {
				snapshot.Slots = append(snapshot.Slots, activeSlotFromZ(z, cmd.target.accountID, true))
			}
		}
	}

	if err := c.fillSlotHolders(ctx, snapshot.Slots); err != nil {
		return nil, err
	}
	service.SortActiveSlots(snapshot.Slots)
	return snapshot, nil
}

func activeSlotFromZ(z redis.Z, accountID int64, live bool) service.ActiveSlot {
	slotID, _ := z.Member.(string)
	return service.ActiveSlot{
		SlotID:    slotID,
		Live:      live,
		AccountID: accountID,
		StartedAt: int64(z.Score),
	}
}

// fillSlotHolders 用 MGET 批量取回普通槽位的占用者详情；Live 租约没有详情。
func (c *concurrencyCache) fillSlotHolders(ctx context.Context, slots []service.ActiveSlot) error {
	indexes := make([]int, 0, len(slots))
	for i := range slots {
		if !slots[i].Live && slots[i].SlotID != "" {
			indexes = append(indexes, i)
		}
	}
	for start := 0; start < len(indexes); start += slotListChunkSize {
		end := min(start+slotListChunkSize, len(indexes))
		keys := make([]string, 0, end-start)
		for _, idx := range indexes[start:end] {
			keys = append(keys, slotHolderKey(slots[idx].SlotID))
		}
		values, err := c.rdb.MGet(ctx, keys...).Result()
		if err != nil {
			return fmt.Errorf("read slot holders: %w", err)
		}
		for offset, value := range values {
			raw, ok := value.(string)
			if !ok || raw == "" {
				continue
			}
			var holder service.SlotHolder
			if json.Unmarshal([]byte(raw), &holder) != nil {
				continue
			}
			slot := &slots[indexes[start+offset]]
			slot.Holder = &holder
			// 用户槽位键里没有账号，所在账号来自占账号槽位时回写的详情。
			if slot.AccountID == 0 {
				slot.AccountID = holder.AccountID
			}
		}
	}
	return nil
}
