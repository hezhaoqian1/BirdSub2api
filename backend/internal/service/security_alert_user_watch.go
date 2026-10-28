package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 用户管理页按单个用户设置/取消余额关注。只改名单里这一个用户，不会覆盖整份告警配置。

const (
	securityAlertKindUserWatch = "user_balance_watch"

	// UserBalanceWatchNoteMaxLength 关注备注的最大字符数。
	UserBalanceWatchNoteMaxLength = 60
	// UserBalanceWatchThresholdMax 提醒线上限（美元）。
	UserBalanceWatchThresholdMax = 1_000_000
)

var (
	ErrUserBalanceWatchThresholdInvalid = infraerrors.BadRequest("USER_BALANCE_WATCH_THRESHOLD_INVALID", "threshold must be greater than 0 and at most 1000000")
	ErrUserBalanceWatchNoteTooLong      = infraerrors.BadRequest("USER_BALANCE_WATCH_NOTE_TOO_LONG", "note must be at most 60 characters")
	ErrUserBalanceWatchLimitReached     = infraerrors.BadRequest("USER_BALANCE_WATCH_LIMIT_REACHED", "too many watched users")
)

// UserBalanceWatchList 用户管理页需要的关注名单与告警通道状态。
type UserBalanceWatchList struct {
	// Enabled 告警推送总开关；关闭时关注用户也不会推送。
	Enabled bool `json:"enabled"`
	// WebhookConfigured 是否已配置企业微信机器人。
	WebhookConfigured bool               `json:"wecom_webhook_configured"`
	Watches           []UserBalanceWatch `json:"watches"`
}

// SetUserBalanceWatchInput 设置单个用户的余额关注。
type SetUserBalanceWatchInput struct {
	UserID    int64
	Threshold float64
	Note      string
	Operator  string
}

// ListUserBalanceWatches 返回当前关注名单。
func (s *SecurityAlertService) ListUserBalanceWatches(ctx context.Context) (UserBalanceWatchList, error) {
	if err := s.requireSettings(); err != nil {
		return UserBalanceWatchList{}, err
	}
	cfg, webhook, err := s.loadConfig(ctx)
	if err != nil {
		return UserBalanceWatchList{}, err
	}
	return UserBalanceWatchList{
		Enabled:           cfg.Enabled,
		WebhookConfigured: webhook != "",
		Watches:           cfg.UserBalanceWatches,
	}, nil
}

// SetUserBalanceWatch 新增或修改一个用户的余额提醒线。提醒线变化后该用户的告警状态清零，
// 下一轮检查（约 1 分钟内）按新提醒线重新判断：已经低于新提醒线的会立即推送一次。
func (s *SecurityAlertService) SetUserBalanceWatch(ctx context.Context, in SetUserBalanceWatchInput) (UserBalanceWatch, error) {
	if in.UserID <= 0 {
		return UserBalanceWatch{}, ErrUserNotFound
	}
	if math.IsNaN(in.Threshold) || math.IsInf(in.Threshold, 0) {
		return UserBalanceWatch{}, ErrUserBalanceWatchThresholdInvalid
	}
	threshold := math.Round(in.Threshold*100) / 100
	if threshold <= 0 || threshold > UserBalanceWatchThresholdMax {
		return UserBalanceWatch{}, ErrUserBalanceWatchThresholdInvalid
	}
	note := strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(note) > UserBalanceWatchNoteMaxLength {
		return UserBalanceWatch{}, ErrUserBalanceWatchNoteTooLong
	}
	if err := s.requireSettings(); err != nil {
		return UserBalanceWatch{}, err
	}

	users, err := s.loadWatchedUsers(ctx, []UserBalanceWatch{{UserID: in.UserID}})
	if err != nil {
		return UserBalanceWatch{}, err
	}
	user, ok := users[in.UserID]
	if !ok {
		return UserBalanceWatch{}, ErrUserNotFound
	}

	s.watchMu.Lock()
	defer s.watchMu.Unlock()

	cfg, _, err := s.loadConfig(ctx)
	if err != nil {
		return UserBalanceWatch{}, err
	}
	next := UserBalanceWatch{UserID: in.UserID, Threshold: threshold, Note: note}
	var prev *UserBalanceWatch
	for i := range cfg.UserBalanceWatches {
		if cfg.UserBalanceWatches[i].UserID == in.UserID {
			old := cfg.UserBalanceWatches[i]
			prev = &old
			cfg.UserBalanceWatches[i] = next
			break
		}
	}
	if prev == nil {
		if len(cfg.UserBalanceWatches) >= securityAlertMaxUserWatches {
			return UserBalanceWatch{}, ErrUserBalanceWatchLimitReached
		}
		cfg.UserBalanceWatches = append(cfg.UserBalanceWatches, next)
	} else if *prev == next {
		return next, nil
	}
	if err := s.saveUserBalanceWatches(ctx, cfg); err != nil {
		return UserBalanceWatch{}, err
	}
	if prev == nil || prev.Threshold != next.Threshold {
		s.resetUserBalanceAlertState(ctx, in.UserID)
	}

	title := "已关注用户余额"
	thresholdLine := "提醒线 " + formatBalanceAmount(threshold, "USD")
	if prev != nil {
		title = "关注用户余额已修改"
		if prev.Threshold != threshold {
			thresholdLine += "（原 " + formatBalanceAmount(prev.Threshold, "USD") + "）"
		}
	}
	s.emitLogged(ctx, securityAlertDraft{
		Kind:     securityAlertKindUserWatch,
		Severity: SecurityAlertInfo,
		Title:    title,
		Lines: []string{
			"用户：" + watchedUserLabel(in.UserID, user.Email, note),
			thresholdLine + "，当前余额 " + formatBalanceAmount(user.Balance, "USD"),
			"操作人：" + sanitizeSecurityText(in.Operator, 80),
		},
	})
	return next, nil
}

// RemoveUserBalanceWatch 取消关注；用户不在名单里时直接返回成功。
func (s *SecurityAlertService) RemoveUserBalanceWatch(ctx context.Context, userID int64, operator string) error {
	if userID <= 0 {
		return ErrUserNotFound
	}
	if err := s.requireSettings(); err != nil {
		return err
	}

	s.watchMu.Lock()
	defer s.watchMu.Unlock()

	cfg, _, err := s.loadConfig(ctx)
	if err != nil {
		return err
	}
	kept := make([]UserBalanceWatch, 0, len(cfg.UserBalanceWatches))
	var removed *UserBalanceWatch
	for i := range cfg.UserBalanceWatches {
		if cfg.UserBalanceWatches[i].UserID == userID {
			old := cfg.UserBalanceWatches[i]
			removed = &old
			continue
		}
		kept = append(kept, cfg.UserBalanceWatches[i])
	}
	if removed == nil {
		return nil
	}
	cfg.UserBalanceWatches = kept
	if err := s.saveUserBalanceWatches(ctx, cfg); err != nil {
		return err
	}
	s.resetUserBalanceAlertState(ctx, userID)

	email := ""
	if users, err := s.loadWatchedUsers(ctx, []UserBalanceWatch{{UserID: userID}}); err == nil {
		email = users[userID].Email
	}
	s.emitLogged(ctx, securityAlertDraft{
		Kind:     securityAlertKindUserWatch,
		Severity: SecurityAlertInfo,
		Title:    "已取消关注用户余额",
		Lines: []string{
			"用户：" + watchedUserLabel(userID, email, removed.Note),
			"原提醒线 " + formatBalanceAmount(removed.Threshold, "USD"),
			"操作人：" + sanitizeSecurityText(operator, 80),
		},
	})
	return nil
}

func (s *SecurityAlertService) requireSettings() error {
	if s == nil || s.settings == nil || s.settings.settingRepo == nil {
		return infraerrors.ServiceUnavailable("SERVICE_UNAVAILABLE", "security alert service unavailable")
	}
	return nil
}

func (s *SecurityAlertService) saveUserBalanceWatches(ctx context.Context, cfg SecurityAlertConfig) error {
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return infraerrors.BadRequest("INVALID_SECURITY_ALERT_CONFIG", err.Error())
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := s.settings.settingRepo.Set(ctx, SettingKeySecurityAlertConfig, string(raw)); err != nil {
		return err
	}
	s.refreshConfig(ctx, true)
	return nil
}

// resetUserBalanceAlertState 清掉用户的告警状态，让下一轮检查按新提醒线重新判断。
func (s *SecurityAlertService) resetUserBalanceAlertState(ctx context.Context, userID int64) {
	if s.db == nil {
		return
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM security_alert_state WHERE subject = $1`,
		"user:"+strconv.FormatInt(userID, 10)); err != nil {
		slog.Warn("[SecurityAlert] failed to reset user alert state", "user_id", userID, "error", err)
	}
}

func watchedUserLabel(userID int64, email, note string) string {
	label := fmt.Sprintf("#%d %s", userID, sanitizeSecurityText(email, 60))
	if note != "" {
		label += "（" + sanitizeSecurityText(note, 60) + "）"
	}
	return strings.TrimSpace(label)
}
