package service

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func rechargeTestSnap(cfg SecurityAlertConfig) securityRuleSnapshot {
	return securityRuleSnapshot{cfg: cfg}
}

func expectRechargeAlert(mock sqlmock.Sqlmock, dedupe, severity, title string, body ...string) {
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(dedupe, securityAlertKindRecharge, severity, title, bodyContains(body)).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

func TestCheckRechargesPushesCompletedExpiredAndStuck(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC) // 北京 14:00
	svc.now = func() time.Time { return now }

	mock.ExpectQuery(regexp.QuoteMeta("WHERE o.status = 'COMPLETED' AND o.completed_at > $1")).
		WithArgs(now.Add(-rechargeCompletedWindow), rechargeScanLimit).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "amount", "pay_amount", "bonus", "payment_type", "order_type", "no", "completed_at", "balance"}).
			AddRow(int64(157), int64(57), "big@example.com", 2100.0, 2000.0, 100.0, "alipay", "balance", "BS20261101xyz", now.Add(-2*time.Minute), 2350.5))
	expectRechargeAlert(mock, "recharge:done:157", "info", "用户充值 ¥2000.00",
		"用户：big@example.com（#57）", "实付：¥2000.00（支付宝）", "到账：$2100.00（含赠送 $100.00）", "充值后余额：$2350.50",
		"订单：BS20261101xyz（#157），完成于 2026-11-01 13:58:00")

	mock.ExpectQuery(regexp.QuoteMeta("WHERE o.status = 'EXPIRED' AND o.pay_amount >= $1")).
		WithArgs(500.0, now.Add(-rechargeExpiredGrace), now.Add(-rechargeExpiredWindow), rechargeScanLimit).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "pay_amount", "payment_type", "no", "created_at", "updated_at"}).
			AddRow(int64(156), int64(57), "big@example.com", 2000.0, "alipay", "BS20261101abc", now.Add(-24*time.Minute), now.Add(-18*time.Minute)))
	expectRechargeAlert(mock, "recharge:expired:156", "warning", "大额充值订单过期未付 ¥2000.00",
		"金额：¥2000.00（支付宝）", "下单 2026-11-01 13:36:00，过期 2026-11-01 13:42:00", "订单：BS20261101abc（#156）")

	mock.ExpectQuery(regexp.QuoteMeta("WHERE o.paid_at IS NOT NULL AND o.completed_at IS NULL")).
		WithArgs(now.Add(-10*time.Minute), now.Add(-rechargeStuckLookback), rechargeScanLimit).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "pay_amount", "amount", "payment_type", "no", "status", "paid_at", "reason"}).
			AddRow(int64(160), int64(82), "", 500.0, 500.0, "alipay", "", "FAILED", now.Add(-25*time.Minute), "redeem balance: timeout"))
	expectRechargeAlert(mock, "recharge:stuck:160", "critical", "用户已付款 ¥500.00 但未到账",
		"用户：#82", "实付：¥500.00（支付宝），应到账 $500.00", "已过 25 分钟 仍未到账（订单状态 FAILED）", "失败原因：redeem balance: timeout")

	svc.checkRecharges(context.Background(), rechargeTestSnap(DefaultSecurityAlertConfig()))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckRechargesRespectsSwitches(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	cfg := DefaultSecurityAlertConfig()
	cfg.RechargeNotify = false
	cfg.RechargeExpiredNotifyCNY = 0
	cfg.RechargeStuckMinutes = 0
	svc.checkRecharges(context.Background(), rechargeTestSnap(cfg))
	require.NoError(t, mock.ExpectationsWereMet()) // 全部关闭时不查库
}

func TestRechargeSubscriptionOrderSkipsBalanceLines(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	mock.ExpectQuery(regexp.QuoteMeta("WHERE o.status = 'COMPLETED'")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "amount", "pay_amount", "bonus", "payment_type", "order_type", "no", "completed_at", "balance"}).
			AddRow(int64(9), int64(3), "sub@example.com", 99.0, 99.0, 0.0, "wxpay", "subscription", "", now, 0.0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs("recharge:done:9", securityAlertKindRecharge, "info", "用户充值 ¥99.00", bodyContains{"实付：¥99.00（微信）", "类型：subscription"}).
		WillReturnResult(sqlmock.NewResult(1, 1))
	cfg := DefaultSecurityAlertConfig()
	cfg.RechargeExpiredNotifyCNY = 0
	cfg.RechargeStuckMinutes = 0
	svc.checkRecharges(context.Background(), rechargeTestSnap(cfg))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildRechargeDigestLine(t *testing.T) {
	svc, mock, _ := newUserWatchTestService(t, "", "")
	start := time.Date(2026, 10, 31, 16, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	mock.ExpectQuery(regexp.QuoteMeta("FROM payment_orders WHERE")).WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c", "d", "e"}).AddRow(3, 2, 3400.0, 1, 2000.0))
	mock.ExpectQuery(regexp.QuoteMeta("FROM redeem_codes WHERE type = 'admin_balance'")).WithArgs(start, end).
		WillReturnRows(sqlmock.NewRows([]string{"a", "b", "c"}).AddRow(3, 8000.0, -5536.0))
	line := svc.buildRechargeDigestLine(context.Background(), start, end)
	require.Equal(t, "充值：在线 3 笔 ¥3400.00（2 人），过期未付 1 笔 ¥2000.00；后台加余额 3 笔 $8000.00（含扣减 $5536.00）", line)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRechargeConfigDefaultsAndValidation(t *testing.T) {
	cfg := DefaultSecurityAlertConfig()
	require.True(t, cfg.RechargeNotify)
	require.Equal(t, 500.0, cfg.RechargeExpiredNotifyCNY)
	require.Equal(t, 10, cfg.RechargeStuckMinutes)
	cfg.RechargeStuckMinutes = -1
	require.Error(t, cfg.Validate())
	cfg.RechargeStuckMinutes = 0
	cfg.RechargeExpiredNotifyCNY = -5
	require.Error(t, cfg.Validate())
}
