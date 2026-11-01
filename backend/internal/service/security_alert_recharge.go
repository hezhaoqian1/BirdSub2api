package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// 用户充值提醒（运营告警，同一个企业微信机器人）：
//   - 在线充值成功：每笔推一次（用户、实付、到账、充值后余额）；
//   - 大额订单过期没付成：金额达到阈值的订单过期 10 分钟后，如果该用户这段时间里没有别的订单付款成功，推一次；
//   - 付了款但没到账：支付平台已确认付款（paid_at 有值），超过 N 分钟订单还没完成，推一次。
// 由 leader 每分钟扫一遍 payment_orders；每类事件按订单 ID 写 dedupe_key，同一订单同一事件只推一次。
// 每日日报里另加一行前一天的充值汇总。

const (
	securityAlertKindRecharge = "user_recharge"
	// rechargeCompletedWindow 只推最近这段时间里完成的订单，上线或重启时不会把历史订单全推一遍。
	rechargeCompletedWindow = 30 * time.Minute
	// rechargeExpiredGrace 订单过期后先等这么久，看客户是不是重新下单付掉了。
	rechargeExpiredGrace  = 10 * time.Minute
	rechargeExpiredWindow = 40 * time.Minute
	rechargeStuckLookback = 24 * time.Hour
	rechargeScanLimit     = 50
)

func rechargePaymentLabel(paymentType string) string {
	switch strings.ToLower(strings.TrimSpace(paymentType)) {
	case "alipay", "alipay_direct", "alipay_qr":
		return "支付宝"
	case "wxpay", "wechat", "wechatpay", "wxpay_direct":
		return "微信"
	case "stripe":
		return "Stripe"
	case "":
		return "未知方式"
	default:
		return sanitizeSecurityText(paymentType, 20)
	}
}

func rechargeUserLabel(userID int64, email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return fmt.Sprintf("#%d", userID)
	}
	return fmt.Sprintf("%s（#%d）", sanitizeSecurityText(email, 80), userID)
}

func rechargeOrderLabel(orderID int64, outTradeNo string) string {
	if no := strings.TrimSpace(outTradeNo); no != "" {
		return sanitizeSecurityText(no, 64) + "（#" + strconv.FormatInt(orderID, 10) + "）"
	}
	return "#" + strconv.FormatInt(orderID, 10)
}

// checkRecharges 扫描充值事件；三类提醒各有开关。
func (s *SecurityAlertService) checkRecharges(ctx context.Context, snap securityRuleSnapshot) {
	cfg := snap.cfg
	now := s.now()
	if cfg.RechargeNotify {
		s.checkCompletedRecharges(ctx, now)
	}
	if cfg.RechargeExpiredNotifyCNY > 0 {
		s.checkExpiredRecharges(ctx, now, cfg.RechargeExpiredNotifyCNY)
	}
	if cfg.RechargeStuckMinutes > 0 {
		s.checkStuckRecharges(ctx, now, cfg.RechargeStuckMinutes)
	}
}

func (s *SecurityAlertService) checkCompletedRecharges(ctx context.Context, now time.Time) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.user_id, COALESCE(u.email, o.user_email, ''), o.amount, o.pay_amount,
 COALESCE(o.bonus_amount, 0), o.payment_type, o.order_type, COALESCE(o.out_trade_no, ''), o.completed_at, COALESCE(u.balance, 0)
 FROM payment_orders o LEFT JOIN users u ON u.id = o.user_id
 WHERE o.status = 'COMPLETED' AND o.completed_at > $1
 ORDER BY o.completed_at LIMIT $2`, now.Add(-rechargeCompletedWindow), rechargeScanLimit)
	if err != nil {
		slog.Warn("[SecurityAlert] recharge scan failed", "error", err)
		return
	}
	var drafts []securityAlertDraft
	for rows.Next() {
		var (
			id, userID                    int64
			email, payType, orderType, no string
			amount, payAmount, bonus      float64
			balance                       float64
			completedAt                   time.Time
		)
		if err := rows.Scan(&id, &userID, &email, &amount, &payAmount, &bonus, &payType, &orderType, &no, &completedAt, &balance); err != nil {
			continue
		}
		lines := []string{
			"用户：" + rechargeUserLabel(userID, email),
			fmt.Sprintf("实付：¥%.2f（%s）", payAmount, rechargePaymentLabel(payType)),
		}
		if orderType == "" || orderType == "balance" {
			credit := fmt.Sprintf("到账：$%.2f", amount)
			if bonus > 0 {
				credit += fmt.Sprintf("（含赠送 $%.2f）", bonus)
			}
			lines = append(lines, credit, fmt.Sprintf("充值后余额：$%.2f", balance))
		} else {
			lines = append(lines, "类型："+sanitizeSecurityText(orderType, 30))
		}
		lines = append(lines, "订单："+rechargeOrderLabel(id, no)+"，完成于 "+formatSecurityTime(completedAt, s.loc))
		drafts = append(drafts, securityAlertDraft{
			DedupeKey: "recharge:done:" + strconv.FormatInt(id, 10),
			Kind:      securityAlertKindRecharge,
			Severity:  SecurityAlertInfo,
			Title:     fmt.Sprintf("用户充值 ¥%.2f", payAmount),
			Lines:     lines,
		})
	}
	_ = rows.Close()
	for _, d := range drafts {
		s.emitLogged(ctx, d)
	}
}

func (s *SecurityAlertService) checkExpiredRecharges(ctx context.Context, now time.Time, minCNY float64) {
	// 过期满 10 分钟（不超过 40 分钟）的大额订单；同一用户在该订单之后有付款成功的订单则不提醒。
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.user_id, COALESCE(u.email, o.user_email, ''), o.pay_amount, o.payment_type,
 COALESCE(o.out_trade_no, ''), o.created_at, o.updated_at
 FROM payment_orders o LEFT JOIN users u ON u.id = o.user_id
 WHERE o.status = 'EXPIRED' AND o.pay_amount >= $1 AND o.updated_at <= $2 AND o.updated_at > $3
   AND NOT EXISTS (SELECT 1 FROM payment_orders c WHERE c.user_id = o.user_id AND c.status = 'COMPLETED' AND c.created_at >= o.created_at)
 ORDER BY o.updated_at LIMIT $4`, minCNY, now.Add(-rechargeExpiredGrace), now.Add(-rechargeExpiredWindow), rechargeScanLimit)
	if err != nil {
		slog.Warn("[SecurityAlert] expired recharge scan failed", "error", err)
		return
	}
	var drafts []securityAlertDraft
	for rows.Next() {
		var (
			id, userID           int64
			email, payType, no   string
			payAmount            float64
			createdAt, expiredAt time.Time
		)
		if err := rows.Scan(&id, &userID, &email, &payAmount, &payType, &no, &createdAt, &expiredAt); err != nil {
			continue
		}
		drafts = append(drafts, securityAlertDraft{
			DedupeKey: "recharge:expired:" + strconv.FormatInt(id, 10),
			Kind:      securityAlertKindRecharge,
			Severity:  SecurityAlertWarning,
			Title:     fmt.Sprintf("大额充值订单过期未付 ¥%.2f", payAmount),
			Lines: []string{
				"用户：" + rechargeUserLabel(userID, email),
				fmt.Sprintf("金额：¥%.2f（%s）", payAmount, rechargePaymentLabel(payType)),
				"下单 " + formatSecurityTime(createdAt, s.loc) + "，过期 " + formatSecurityTime(expiredAt, s.loc) + "，没收到付款，之后也没有别的订单付款成功",
				"订单：" + rechargeOrderLabel(id, no),
				"可能是客户没付完，可以联系跟进；如果客户说已付款，请到支付平台核对这个订单号。",
			},
		})
	}
	_ = rows.Close()
	for _, d := range drafts {
		s.emitLogged(ctx, d)
	}
}

func (s *SecurityAlertService) checkStuckRecharges(ctx context.Context, now time.Time, minutes int) {
	rows, err := s.db.QueryContext(ctx, `SELECT o.id, o.user_id, COALESCE(u.email, o.user_email, ''), o.pay_amount, o.amount,
 o.payment_type, COALESCE(o.out_trade_no, ''), o.status, o.paid_at, COALESCE(o.failed_reason, '')
 FROM payment_orders o LEFT JOIN users u ON u.id = o.user_id
 WHERE o.paid_at IS NOT NULL AND o.completed_at IS NULL
   AND o.status IN ('PAID', 'RECHARGING', 'FAILED')
   AND o.paid_at <= $1 AND o.paid_at > $2
 ORDER BY o.paid_at LIMIT $3`, now.Add(-time.Duration(minutes)*time.Minute), now.Add(-rechargeStuckLookback), rechargeScanLimit)
	if err != nil {
		slog.Warn("[SecurityAlert] stuck recharge scan failed", "error", err)
		return
	}
	var drafts []securityAlertDraft
	for rows.Next() {
		var (
			id, userID                         int64
			email, payType, no, status, reason string
			payAmount, amount                  float64
			paidAt                             time.Time
		)
		if err := rows.Scan(&id, &userID, &email, &payAmount, &amount, &payType, &no, &status, &paidAt, &reason); err != nil {
			continue
		}
		lines := []string{
			"用户：" + rechargeUserLabel(userID, email),
			fmt.Sprintf("实付：¥%.2f（%s），应到账 $%.2f", payAmount, rechargePaymentLabel(payType), amount),
			"付款于 " + formatSecurityTime(paidAt, s.loc) + "，已过 " + formatAlertDuration(now.Sub(paidAt)) + " 仍未到账（订单状态 " + sanitizeSecurityText(status, 20) + "）",
			"订单：" + rechargeOrderLabel(id, no),
		}
		if strings.TrimSpace(reason) != "" {
			lines = append(lines, "失败原因："+sanitizeSecurityText(reason, 120))
		}
		lines = append(lines, "处理：到后台支付订单里重试发放，或核对后手动给用户加余额。")
		drafts = append(drafts, securityAlertDraft{
			DedupeKey: "recharge:stuck:" + strconv.FormatInt(id, 10),
			Kind:      securityAlertKindRecharge,
			Severity:  SecurityAlertCritical,
			Title:     fmt.Sprintf("用户已付款 ¥%.2f 但未到账", payAmount),
			Lines:     lines,
		})
	}
	_ = rows.Close()
	for _, d := range drafts {
		s.emitLogged(ctx, d)
	}
}

// buildRechargeDigestLine 日报里前一天的充值汇总。
func (s *SecurityAlertService) buildRechargeDigestLine(ctx context.Context, start, end time.Time) string {
	var paidCount, paidUsers, expiredCount, adminCount int
	var paidCNY, expiredCNY, adminAdd, adminSub float64
	if err := s.db.QueryRowContext(ctx, `SELECT
 COUNT(*) FILTER (WHERE status = 'COMPLETED' AND completed_at >= $1 AND completed_at < $2),
 COUNT(DISTINCT user_id) FILTER (WHERE status = 'COMPLETED' AND completed_at >= $1 AND completed_at < $2),
 COALESCE(SUM(pay_amount) FILTER (WHERE status = 'COMPLETED' AND completed_at >= $1 AND completed_at < $2), 0),
 COUNT(*) FILTER (WHERE status = 'EXPIRED' AND created_at >= $1 AND created_at < $2),
 COALESCE(SUM(pay_amount) FILTER (WHERE status = 'EXPIRED' AND created_at >= $1 AND created_at < $2), 0)
 FROM payment_orders WHERE (completed_at >= $1 AND completed_at < $2) OR (created_at >= $1 AND created_at < $2)`,
		start, end).Scan(&paidCount, &paidUsers, &paidCNY, &expiredCount, &expiredCNY); err != nil {
		slog.Warn("[SecurityAlert] recharge digest failed", "error", err)
		return ""
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE value > 0),
 COALESCE(SUM(value) FILTER (WHERE value > 0), 0), COALESCE(SUM(value) FILTER (WHERE value < 0), 0)
 FROM redeem_codes WHERE type = 'admin_balance' AND used_at >= $1 AND used_at < $2`, start, end).Scan(&adminCount, &adminAdd, &adminSub); err != nil {
		slog.Warn("[SecurityAlert] admin top-up digest failed", "error", err)
		return ""
	}
	line := fmt.Sprintf("充值：在线 %d 笔 ¥%.2f（%d 人）", paidCount, paidCNY, paidUsers)
	if expiredCount > 0 {
		line += fmt.Sprintf("，过期未付 %d 笔 ¥%.2f", expiredCount, expiredCNY)
	}
	line += fmt.Sprintf("；后台加余额 %d 笔 $%.2f", adminCount, adminAdd)
	if adminSub < 0 {
		line += fmt.Sprintf("（含扣减 $%.2f）", -adminSub)
	}
	return line
}
