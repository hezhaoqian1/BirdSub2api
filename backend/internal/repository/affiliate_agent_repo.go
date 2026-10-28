package repository

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// GrantAgentCredit 代理给自己邀请的用户发测试金。在一个事务里：
//  1. 确认被发放人是这个代理邀请的；
//  2. 用 operation_id 占一条发放记录（同标识已存在时返回首次结果）；
//  3. 锁住代理的返利档案行，确认是代理；
//  4. 在总额度内扣减额度（不足则整笔回滚）；
//  5. 给被邀请人加余额，并写一条 agent_balance 余额调整记录；
//  6. 把发放后的余额与额度快照写回发放记录。
//
// 发放不产生邀请返利，也不计入被邀请人的累计充值。
func (r *affiliateRepository) GrantAgentCredit(ctx context.Context, agentUserID, inviteeUserID int64, amount float64, notes, operationID string) (*service.AffiliateAgentGrantResult, error) {
	if agentUserID <= 0 || inviteeUserID <= 0 {
		return nil, service.ErrAffiliateAgentInviteeNotFound
	}
	if amount <= 0 {
		return nil, service.ErrAffiliateAgentAmountInvalid
	}
	if operationID == "" {
		return nil, service.ErrIdempotencyKeyRequired
	}

	var result *service.AffiliateAgentGrantResult
	err := r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		// 邀请关系绑定后不会改变，先校验它：重放的请求同样满足，且能挡住不存在的用户 ID。
		invited, err := scanAffiliateBool(txCtx, txClient, `
SELECT EXISTS(SELECT 1 FROM user_affiliates WHERE user_id = $1 AND inviter_id = $2)`, inviteeUserID, agentUserID)
		if err != nil {
			return fmt.Errorf("check agent invitee: %w", err)
		}
		if !invited {
			return service.ErrAffiliateAgentInviteeNotFound
		}

		grantID, claimed, err := claimAffiliateAgentGrant(txCtx, txClient, agentUserID, inviteeUserID, amount, notes, operationID)
		if err != nil {
			return err
		}
		if !claimed {
			result, err = findAffiliateAgentGrantByOperation(txCtx, txClient, agentUserID, inviteeUserID, amount, operationID)
			return err
		}

		enabled, found, err := scanAffiliateAgentEnabledForUpdate(txCtx, txClient, agentUserID)
		if err != nil {
			return err
		}
		if !found || !enabled {
			return service.ErrAffiliateAgentNotEnabled
		}

		creditLimit, creditUsed, deducted, err := deductAffiliateAgentCredit(txCtx, txClient, agentUserID, amount)
		if err != nil {
			return err
		}
		if !deducted {
			return service.ErrAffiliateAgentCreditInsufficient
		}

		affected, err := txClient.User.Update().
			Where(user.IDEQ(inviteeUserID)).
			AddBalance(amount).
			Save(txCtx)
		if err != nil {
			return fmt.Errorf("credit invitee balance by agent grant: %w", err)
		}
		if affected == 0 {
			return service.ErrAffiliateAgentInviteeNotFound
		}

		balanceAfter, err := queryUserBalance(txCtx, txClient, inviteeUserID)
		if err != nil {
			return err
		}

		code, err := service.GenerateRedeemCode()
		if err != nil {
			return fmt.Errorf("generate agent grant record code: %w", err)
		}
		if _, err := txClient.RedeemCode.Create().
			SetCode(code).
			SetType(service.RedeemTypeAgentBalance).
			SetValue(amount).
			SetStatus(service.StatusUsed).
			SetUsedBy(inviteeUserID).
			SetUsedAt(time.Now()).
			SetNotes(notes).
			Save(txCtx); err != nil {
			return fmt.Errorf("record agent grant balance adjustment: %w", err)
		}

		if _, err := txClient.ExecContext(txCtx, `
UPDATE affiliate_agent_grants
SET invitee_balance_after = $1,
    agent_credit_limit_after = $2,
    agent_credit_used_after = $3
WHERE id = $4`, balanceAfter, creditLimit, creditUsed, grantID); err != nil {
			return fmt.Errorf("record agent grant snapshot: %w", err)
		}

		result = &service.AffiliateAgentGrantResult{
			GrantID:              grantID,
			InviteeUserID:        inviteeUserID,
			Amount:               amount,
			AffiliateAgentCredit: agentCreditSnapshot(creditLimit, creditUsed),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func agentCreditSnapshot(limit, used float64) service.AffiliateAgentCredit {
	return service.AffiliateAgentCredit{
		CreditLimit:     limit,
		CreditUsed:      used,
		CreditRemaining: math.Max(0, limit-used),
	}
}

// claimAffiliateAgentGrant 写入一条带 operation_id 的发放记录占位。同标识的记录
// 已存在时 claimed 为 false；同标识的并发事务在唯一索引上等待先写入者结束。
func claimAffiliateAgentGrant(ctx context.Context, client affiliateQueryExecer, agentUserID, inviteeUserID int64, amount float64, notes, operationID string) (int64, bool, error) {
	rows, err := client.QueryContext(ctx, `
INSERT INTO affiliate_agent_grants (agent_user_id, invitee_user_id, amount, notes, operation_id, created_at)
VALUES ($1, $2, $3, $4, $5, NOW())
ON CONFLICT (operation_id) WHERE operation_id IS NOT NULL DO NOTHING
RETURNING id`, agentUserID, inviteeUserID, amount, notes, operationID)
	if err != nil {
		return 0, false, fmt.Errorf("claim agent grant: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, false, fmt.Errorf("claim agent grant: %w", err)
		}
		return 0, false, nil
	}
	var grantID int64
	if err := rows.Scan(&grantID); err != nil {
		return 0, false, err
	}
	return grantID, true, rows.Close()
}

// findAffiliateAgentGrantByOperation 读取 operation_id 已对应的发放记录：代理、
// 被邀请人与金额都一致时返回首次发放的结果（Replayed=true），否则返回
// ErrIdempotencyKeyConflict。
func findAffiliateAgentGrantByOperation(ctx context.Context, client affiliateQueryExecer, agentUserID, inviteeUserID int64, amount float64, operationID string) (*service.AffiliateAgentGrantResult, error) {
	rows, err := client.QueryContext(ctx, `
SELECT id,
       agent_user_id IS NOT DISTINCT FROM $2
           AND invitee_user_id IS NOT DISTINCT FROM $3
           AND amount = CAST($4 AS DECIMAL(20,8)),
       amount::double precision,
       COALESCE(agent_credit_limit_after, 0)::double precision,
       COALESCE(agent_credit_used_after, 0)::double precision
FROM affiliate_agent_grants
WHERE operation_id = $1`, operationID, agentUserID, inviteeUserID, amount)
	if err != nil {
		return nil, fmt.Errorf("query agent grant by operation: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("query agent grant by operation: %w", err)
		}
		return nil, service.ErrIdempotencyKeyConflict
	}
	var (
		sameRequest bool
		limit, used float64
	)
	result := &service.AffiliateAgentGrantResult{InviteeUserID: inviteeUserID, Replayed: true}
	if err := rows.Scan(&result.GrantID, &sameRequest, &result.Amount, &limit, &used); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if !sameRequest {
		return nil, service.ErrIdempotencyKeyConflict
	}
	result.AffiliateAgentCredit = agentCreditSnapshot(limit, used)
	return result, nil
}

// scanAffiliateAgentEnabledForUpdate 锁住代理的返利档案行并返回是否为代理。
// 同一代理的并发发放在这把行锁上排队，额度扣减因此不会超发。
func scanAffiliateAgentEnabledForUpdate(ctx context.Context, client affiliateQueryExecer, agentUserID int64) (enabled bool, found bool, err error) {
	rows, err := client.QueryContext(ctx, `
SELECT agent_enabled
FROM user_affiliates
WHERE user_id = $1
FOR UPDATE`, agentUserID)
	if err != nil {
		return false, false, fmt.Errorf("lock agent affiliate row: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return false, false, rows.Err()
	}
	if err := rows.Scan(&enabled); err != nil {
		return false, false, err
	}
	return enabled, true, rows.Close()
}

// deductAffiliateAgentCredit 在剩余额度足够时扣减额度，返回扣减后的总额度与已用额度。
func deductAffiliateAgentCredit(ctx context.Context, client affiliateQueryExecer, agentUserID int64, amount float64) (limit, used float64, deducted bool, err error) {
	rows, err := client.QueryContext(ctx, `
UPDATE user_affiliates
SET agent_credit_used = agent_credit_used + $1,
    updated_at = NOW()
WHERE user_id = $2
  AND agent_enabled = true
  AND agent_credit_limit - agent_credit_used >= CAST($1 AS DECIMAL(20,8))
RETURNING agent_credit_limit::double precision, agent_credit_used::double precision`, amount, agentUserID)
	if err != nil {
		return 0, 0, false, fmt.Errorf("deduct agent credit: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, 0, false, rows.Err()
	}
	if err := rows.Scan(&limit, &used); err != nil {
		return 0, 0, false, err
	}
	return limit, used, true, rows.Close()
}

// SetUserAgent 设置代理开关和测试金总额度；nil 表示该项不变。用户还没有返利档案时先创建。
func (r *affiliateRepository) SetUserAgent(ctx context.Context, userID int64, enabled *bool, creditLimit *float64) error {
	if userID <= 0 {
		return service.ErrUserNotFound
	}
	return r.withTx(ctx, func(txCtx context.Context, txClient *dbent.Client) error {
		if _, err := ensureUserAffiliateWithClient(txCtx, txClient, userID); err != nil {
			return err
		}
		var enabledArg any
		if enabled != nil {
			enabledArg = *enabled
		}
		res, err := txClient.ExecContext(txCtx, `
UPDATE user_affiliates
SET agent_enabled = COALESCE($1::boolean, agent_enabled),
    agent_credit_limit = COALESCE($2::numeric, agent_credit_limit),
    updated_at = NOW()
WHERE user_id = $3`, enabledArg, nullableArg(creditLimit), userID)
		if err != nil {
			return fmt.Errorf("set affiliate agent: %w", err)
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return service.ErrUserNotFound
		}
		return nil
	})
}

// ListAffiliateAgentGrantRecords 列出所有代理测试金发放记录，可按代理、被邀请人、
// 备注搜索。
func (r *affiliateRepository) ListAffiliateAgentGrantRecords(ctx context.Context, filter service.AffiliateRecordFilter) ([]service.AffiliateAgentGrantRecord, int64, error) {
	client := clientFromContext(ctx, r.client)
	where, args := buildAffiliateRecordWhere(filter, "aag.created_at", []string{
		"agent.email", "agent.username", "agent.id::text",
		"invitee.email", "invitee.username", "invitee.id::text",
		"aag.notes",
	})
	baseJoin := `
FROM affiliate_agent_grants aag
LEFT JOIN users agent ON agent.id = aag.agent_user_id
LEFT JOIN users invitee ON invitee.id = aag.invitee_user_id
`

	total, err := queryAffiliateRecordCount(ctx, client, "SELECT COUNT(*) "+baseJoin+where, args...)
	if err != nil {
		return nil, 0, err
	}

	orderBy := buildAffiliateRecordOrderBy(filter, map[string]string{
		"agent":         "agent.email",
		"grant_invitee": "invitee.email",
		"amount":        "aag.amount",
		"created_at":    "aag.created_at",
	}, "aag.created_at")
	args = append(args, filter.PageSize, (filter.Page-1)*filter.PageSize)
	rows, err := client.QueryContext(ctx, `
SELECT aag.id,
       aag.agent_user_id,
       COALESCE(agent.email, ''),
       COALESCE(agent.username, ''),
       aag.invitee_user_id,
       COALESCE(invitee.email, ''),
       COALESCE(invitee.username, ''),
       aag.amount::double precision,
       aag.notes,
       aag.invitee_balance_after::double precision,
       aag.agent_credit_limit_after::double precision,
       aag.agent_credit_used_after::double precision,
       aag.created_at
`+baseJoin+where+`
`+orderBy+`
LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	items := make([]service.AffiliateAgentGrantRecord, 0)
	for rows.Next() {
		var item service.AffiliateAgentGrantRecord
		var agentID, inviteeID sql.NullInt64
		var balanceAfter, limitAfter, usedAfter sql.NullFloat64
		if err := rows.Scan(
			&item.ID,
			&agentID,
			&item.AgentEmail,
			&item.AgentUsername,
			&inviteeID,
			&item.InviteeEmail,
			&item.InviteeUsername,
			&item.Amount,
			&item.Notes,
			&balanceAfter,
			&limitAfter,
			&usedAfter,
			&item.CreatedAt,
		); err != nil {
			return nil, 0, err
		}
		item.AgentUserID = nullableInt64Ptr(agentID)
		item.InviteeUserID = nullableInt64Ptr(inviteeID)
		item.InviteeBalanceAfter = nullableFloat64Ptr(balanceAfter)
		item.AgentCreditLimitAfter = nullableFloat64Ptr(limitAfter)
		item.AgentCreditUsedAfter = nullableFloat64Ptr(usedAfter)
		item.Notes = strings.TrimSpace(item.Notes)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
