//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// mustCreateCommittedAgentPair 在测试事务之外建一个代理和一个由他邀请、余额为 1 的用户，
// 再建一个不是他邀请的用户。GrantAgentCredit 在这些用例里自行开启并提交事务，
// 失败时整笔回滚，与生产调用一致。测试结束时删除这些用户及其发放记录。
func mustCreateCommittedAgentPair(t *testing.T, label string, creditLimit float64) (agent, invitee, stranger *service.User) {
	t.Helper()
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB)

	newUser := func(role string, balance float64) *service.User {
		return mustCreateUser(t, client, &service.User{
			Email:        fmt.Sprintf("affiliate-agent-%s-%s-%d@example.com", label, role, time.Now().UnixNano()),
			PasswordHash: "hash",
			Role:         service.RoleUser,
			Status:       service.StatusActive,
			Balance:      balance,
			Concurrency:  5,
		})
	}
	agent = newUser("agent", 0)
	invitee = newUser("invitee", 1)
	stranger = newUser("stranger", 1)
	t.Cleanup(func() {
		ids := []any{agent.ID, invitee.ID, stranger.ID}
		for _, id := range ids {
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM affiliate_agent_grants WHERE agent_user_id = $1 OR invitee_user_id = $1", id)
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM redeem_codes WHERE used_by = $1", id)
			_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = $1", id)
		}
	})

	bound, err := repo.BindInviter(ctx, invitee.ID, agent.ID)
	require.NoError(t, err)
	require.True(t, bound)
	_, err = repo.EnsureUserAffiliate(ctx, stranger.ID)
	require.NoError(t, err)

	enabled := true
	require.NoError(t, repo.SetUserAgent(ctx, agent.ID, &enabled, &creditLimit))
	return agent, invitee, stranger
}

func userBalance(t *testing.T, client *dbent.Client, userID int64) float64 {
	t.Helper()
	return querySingleFloat(t, context.Background(), client, "SELECT balance::double precision FROM users WHERE id = $1", userID)
}

// TestAffiliateRepository_AgentGrant_CreditsInviteeAndRecords 覆盖主路径：扣代理额度、
// 加被邀请人余额、写发放记录（带快照）和 agent_balance 余额调整记录；不产生返利、
// 不计入累计充值；邀请列表里能看到已发金额。
func TestAffiliateRepository_AgentGrant_CreditsInviteeAndRecords(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB)
	agent, invitee, _ := mustCreateCommittedAgentPair(t, "main", 50)

	result, err := repo.GrantAgentCredit(ctx, agent.ID, invitee.ID, 20, "trial for team A", newAffiliateWithdrawOperationID("agent-main"))
	require.NoError(t, err)
	require.False(t, result.Replayed)
	require.Equal(t, invitee.ID, result.InviteeUserID)
	require.InDelta(t, 20, result.Amount, 1e-9)
	require.InDelta(t, 50, result.CreditLimit, 1e-9)
	require.InDelta(t, 20, result.CreditUsed, 1e-9)
	require.InDelta(t, 30, result.CreditRemaining, 1e-9)

	require.InDelta(t, 21, userBalance(t, client, invitee.ID), 1e-9)
	require.InDelta(t, 0, querySingleFloat(t, ctx, client,
		"SELECT total_recharged::double precision FROM users WHERE id = $1", invitee.ID), 1e-9)
	require.Equal(t, 0, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM user_affiliate_ledger WHERE user_id = $1", agent.ID), "agent grants must not accrue rebate")

	require.Equal(t, 1, querySingleInt(t, ctx, client, `
SELECT COUNT(*) FROM redeem_codes
WHERE used_by = $1 AND type = 'agent_balance' AND status = 'used'
  AND value = 20 AND notes = 'trial for team A'`, invitee.ID))
	require.Equal(t, 1, querySingleInt(t, ctx, client, `
SELECT COUNT(*) FROM affiliate_agent_grants
WHERE id = $1 AND agent_user_id = $2 AND invitee_user_id = $3 AND amount = 20
  AND invitee_balance_after = 21 AND agent_credit_limit_after = 50 AND agent_credit_used_after = 20`,
		result.GrantID, agent.ID, invitee.ID))

	summary, err := repo.EnsureUserAffiliate(ctx, agent.ID)
	require.NoError(t, err)
	require.True(t, summary.AgentEnabled)
	require.InDelta(t, 50, summary.AgentCreditLimit, 1e-9)
	require.InDelta(t, 20, summary.AgentCreditUsed, 1e-9)

	invitees, err := repo.ListInvitees(ctx, agent.ID, 10)
	require.NoError(t, err)
	require.Len(t, invitees, 1)
	require.Equal(t, invitee.ID, invitees[0].UserID)
	require.InDelta(t, 20, invitees[0].AgentGranted, 1e-9)

	records, total, err := repo.ListAffiliateAgentGrantRecords(ctx, service.AffiliateRecordFilter{
		Search: "team a", Page: 1, PageSize: 20, SortDesc: true,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, total, int64(1))
	var found *service.AffiliateAgentGrantRecord
	for i := range records {
		if records[i].ID == result.GrantID {
			found = &records[i]
		}
	}
	require.NotNil(t, found, "grant should be listed and searchable by notes")
	require.Equal(t, agent.Email, found.AgentEmail)
	require.Equal(t, invitee.Email, found.InviteeEmail)
	require.Equal(t, "trial for team A", found.Notes)
	require.NotNil(t, found.InviteeBalanceAfter)
	require.InDelta(t, 21, *found.InviteeBalanceAfter, 1e-9)
}

// TestAffiliateRepository_AgentGrant_RejectsAndRollsBack 覆盖拒绝路径：不是自己邀请的用户、
// 额度不足、代理身份被关闭。每种情况都不改余额、不扣额度、不留发放记录。
func TestAffiliateRepository_AgentGrant_RejectsAndRollsBack(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB)
	agent, invitee, stranger := mustCreateCommittedAgentPair(t, "reject", 10)

	_, err := repo.GrantAgentCredit(ctx, agent.ID, stranger.ID, 1, "not mine", newAffiliateWithdrawOperationID("agent-stranger"))
	require.ErrorIs(t, err, service.ErrAffiliateAgentInviteeNotFound)
	require.InDelta(t, 1, userBalance(t, client, stranger.ID), 1e-9)

	_, err = repo.GrantAgentCredit(ctx, agent.ID, invitee.ID, 10.01, "too much", newAffiliateWithdrawOperationID("agent-over"))
	require.ErrorIs(t, err, service.ErrAffiliateAgentCreditInsufficient)
	require.InDelta(t, 1, userBalance(t, client, invitee.ID), 1e-9)

	// 用户 ID 不存在时也按「不是你邀请的用户」处理，而不是数据库外键错误。
	_, err = repo.GrantAgentCredit(ctx, agent.ID, 9_000_000_000, 1, "ghost", newAffiliateWithdrawOperationID("agent-ghost"))
	require.ErrorIs(t, err, service.ErrAffiliateAgentInviteeNotFound)

	disabled := false
	require.NoError(t, repo.SetUserAgent(ctx, agent.ID, &disabled, nil))
	_, err = repo.GrantAgentCredit(ctx, agent.ID, invitee.ID, 1, "disabled", newAffiliateWithdrawOperationID("agent-disabled"))
	require.ErrorIs(t, err, service.ErrAffiliateAgentNotEnabled)
	require.InDelta(t, 1, userBalance(t, client, invitee.ID), 1e-9)

	require.Equal(t, 0, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM affiliate_agent_grants WHERE agent_user_id = $1", agent.ID))
	require.Equal(t, 0, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM redeem_codes WHERE used_by IN ($1, $2) AND type = 'agent_balance'", invitee.ID, stranger.ID))
	require.InDelta(t, 0, querySingleFloat(t, ctx, client,
		"SELECT agent_credit_used::double precision FROM user_affiliates WHERE user_id = $1", agent.ID), 1e-9)

	// 关闭后额度保留；再次开启并调高总额度即可继续发。
	enabled := true
	limit := 30.0
	require.NoError(t, repo.SetUserAgent(ctx, agent.ID, &enabled, &limit))
	result, err := repo.GrantAgentCredit(ctx, agent.ID, invitee.ID, 25, "after raise", newAffiliateWithdrawOperationID("agent-raise"))
	require.NoError(t, err)
	require.InDelta(t, 5, result.CreditRemaining, 1e-9)
}

// TestAffiliateRepository_AgentGrant_ReplayAndConflict 验证同一 operation_id 只发放一次：
// 相同请求重放首次结果；换了金额或被邀请人时以 ErrIdempotencyKeyConflict 拒绝。
func TestAffiliateRepository_AgentGrant_ReplayAndConflict(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB)
	agent, invitee, _ := mustCreateCommittedAgentPair(t, "replay", 100)
	operationID := newAffiliateWithdrawOperationID("agent-replay")

	first, err := repo.GrantAgentCredit(ctx, agent.ID, invitee.ID, 10, "first", operationID)
	require.NoError(t, err)
	second, err := repo.GrantAgentCredit(ctx, agent.ID, invitee.ID, 10, "first", operationID)
	require.NoError(t, err)
	require.True(t, second.Replayed)
	require.Equal(t, first.GrantID, second.GrantID)
	require.InDelta(t, first.CreditUsed, second.CreditUsed, 1e-9)
	require.InDelta(t, 11, userBalance(t, client, invitee.ID), 1e-9)

	_, err = repo.GrantAgentCredit(ctx, agent.ID, invitee.ID, 11, "first", operationID)
	require.True(t, errors.Is(err, service.ErrIdempotencyKeyConflict), "got %v", err)
	require.InDelta(t, 11, userBalance(t, client, invitee.ID), 1e-9)
	require.InDelta(t, 10, querySingleFloat(t, ctx, client,
		"SELECT agent_credit_used::double precision FROM user_affiliates WHERE user_id = $1", agent.ID), 1e-9)
}

// TestAffiliateRepository_AgentGrant_ConcurrentGrantsNeverExceedLimit 并发发放时，
// 额度扣减在代理行锁上串行，总发放不会超过总额度。
func TestAffiliateRepository_AgentGrant_ConcurrentGrantsNeverExceedLimit(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewAffiliateRepository(client, integrationDB)
	agent, invitee, _ := mustCreateCommittedAgentPair(t, "concurrent", 50)

	const workers = 8
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = repo.GrantAgentCredit(ctx, agent.ID, invitee.ID, 10, "burst", newAffiliateWithdrawOperationID(fmt.Sprintf("agent-burst-%d", i)))
		}(i)
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		require.ErrorIs(t, err, service.ErrAffiliateAgentCreditInsufficient)
	}
	require.Equal(t, 5, succeeded)
	require.InDelta(t, 51, userBalance(t, client, invitee.ID), 1e-9)
	require.InDelta(t, 50, querySingleFloat(t, ctx, client,
		"SELECT agent_credit_used::double precision FROM user_affiliates WHERE user_id = $1", agent.ID), 1e-9)
	require.Equal(t, 5, querySingleInt(t, ctx, client,
		"SELECT COUNT(*) FROM affiliate_agent_grants WHERE agent_user_id = $1", agent.ID))
}

// TestAffiliateRepository_AgentAppearsInCustomUserList 代理即使没有自定义码或专属比例，
// 也出现在专属用户列表里，并带上额度。
func TestAffiliateRepository_AgentAppearsInCustomUserList(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	txCtx := dbent.NewTxContext(ctx, tx)
	client := tx.Client()
	repo := NewAffiliateRepository(client, integrationDB)

	agentUser := mustCreateUser(t, client, &service.User{
		Email:        fmt.Sprintf("affiliate-agent-list-%d@example.com", time.Now().UnixNano()),
		PasswordHash: "hash",
		Role:         service.RoleUser, Status: service.StatusActive,
	})
	enabled := true
	limit := 88.5
	require.NoError(t, repo.SetUserAgent(txCtx, agentUser.ID, &enabled, &limit))

	entries, _, err := repo.ListUsersWithCustomSettings(txCtx, service.AffiliateAdminFilter{
		Search: agentUser.Email, Page: 1, PageSize: 20,
	})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.True(t, entries[0].AgentEnabled)
	require.InDelta(t, 88.5, entries[0].AgentCreditLimit, 1e-9)
	require.Zero(t, entries[0].AgentCreditUsed)
	require.False(t, entries[0].AffCodeCustom)
	require.Nil(t, entries[0].AffRebateRatePercent)
}
