package service

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// userWatchSettingRepo 内存版设置存储。
type userWatchSettingRepo struct {
	values map[string]string
}

func (r *userWatchSettingRepo) Get(_ context.Context, key string) (*Setting, error) {
	value, ok := r.values[key]
	if !ok {
		return nil, ErrSettingNotFound
	}
	return &Setting{Key: key, Value: value}, nil
}

func (r *userWatchSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	setting, err := r.Get(ctx, key)
	if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (r *userWatchSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *userWatchSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	result := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			result[key] = value
		}
	}
	return result, nil
}

func (r *userWatchSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func (r *userWatchSettingRepo) GetAll(_ context.Context) (map[string]string, error) {
	result := make(map[string]string, len(r.values))
	for key, value := range r.values {
		result[key] = value
	}
	return result, nil
}

func (r *userWatchSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func newUserWatchTestService(t *testing.T, cfgJSON, webhook string) (*SecurityAlertService, sqlmock.Sqlmock, *userWatchSettingRepo) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &userWatchSettingRepo{values: map[string]string{}}
	if cfgJSON != "" {
		repo.values[SettingKeySecurityAlertConfig] = cfgJSON
	}
	if webhook != "" {
		repo.values[SettingKeySecurityAlertWeComWebhook] = webhook
	}
	svc := NewSecurityAlertService(db, NewSettingService(repo, &config.Config{}), nil)
	return svc, mock, repo
}

func savedUserWatches(t *testing.T, repo *userWatchSettingRepo) []UserBalanceWatch {
	t.Helper()
	var cfg SecurityAlertConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeySecurityAlertConfig]), &cfg))
	return cfg.UserBalanceWatches
}

func expectWatchedUser(mock sqlmock.Sqlmock, id int64, email string, balance float64) {
	mock.ExpectQuery(regexp.QuoteMeta("FROM users WHERE id = ANY")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "email", "username", "balance"}).AddRow(id, email, "", balance))
}

func expectUserWatchNotice(mock sqlmock.Sqlmock, title string, body ...string) {
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO security_alert_events")).
		WithArgs(sqlmock.AnyArg(), securityAlertKindUserWatch, "info", title, bodyContains(body)).
		WillReturnResult(sqlmock.NewResult(1, 1))
}

func expectUserAlertStateReset(mock sqlmock.Sqlmock, subject string) {
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM security_alert_state WHERE subject = $1")).
		WithArgs(subject).WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestSetUserBalanceWatchAddsUserAndResetsState(t *testing.T) {
	svc, mock, repo := newUserWatchTestService(t,
		`{"enabled":true,"trusted_cidrs":["39.144.0.0/16"],"user_balance_watches":[{"user_id":5,"threshold":10}]}`, "")

	expectWatchedUser(mock, 12, "big@example.com", 8.2)
	expectUserAlertStateReset(mock, "user:12")
	expectUserWatchNotice(mock, "已关注用户余额",
		"用户：#12 big@example.com（大客户）", "提醒线 $50.00，当前余额 $8.20", "操作人：admin@example.com")

	watch, err := svc.SetUserBalanceWatch(context.Background(), SetUserBalanceWatchInput{
		UserID: 12, Threshold: 49.996, Note: "  大客户 ", Operator: "admin@example.com",
	})
	require.NoError(t, err)
	require.Equal(t, UserBalanceWatch{UserID: 12, Threshold: 50, Note: "大客户"}, watch)
	require.NoError(t, mock.ExpectationsWereMet())

	require.Equal(t, []UserBalanceWatch{{UserID: 5, Threshold: 10}, {UserID: 12, Threshold: 50, Note: "大客户"}}, savedUserWatches(t, repo))
	// 其他配置字段原样保留。
	require.Contains(t, repo.values[SettingKeySecurityAlertConfig], `"trusted_cidrs":["39.144.0.0/16"]`)
}

func TestSetUserBalanceWatchUpdatesExistingUser(t *testing.T) {
	cfg := `{"enabled":true,"user_balance_watches":[{"user_id":12,"threshold":50,"note":"a"}]}`

	t.Run("threshold change resets state", func(t *testing.T) {
		svc, mock, repo := newUserWatchTestService(t, cfg, "")
		expectWatchedUser(mock, 12, "big@example.com", 80)
		expectUserAlertStateReset(mock, "user:12")
		expectUserWatchNotice(mock, "关注用户余额已修改", "提醒线 $100.00（原 $50.00），当前余额 $80.00")

		_, err := svc.SetUserBalanceWatch(context.Background(), SetUserBalanceWatchInput{UserID: 12, Threshold: 100, Note: "a"})
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
		require.Equal(t, []UserBalanceWatch{{UserID: 12, Threshold: 100, Note: "a"}}, savedUserWatches(t, repo))
	})

	t.Run("note change keeps state", func(t *testing.T) {
		svc, mock, repo := newUserWatchTestService(t, cfg, "")
		expectWatchedUser(mock, 12, "big@example.com", 80)
		expectUserWatchNotice(mock, "关注用户余额已修改", "用户：#12 big@example.com（b）", "提醒线 $50.00，当前余额 $80.00")

		_, err := svc.SetUserBalanceWatch(context.Background(), SetUserBalanceWatchInput{UserID: 12, Threshold: 50, Note: "b"})
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
		require.Equal(t, []UserBalanceWatch{{UserID: 12, Threshold: 50, Note: "b"}}, savedUserWatches(t, repo))
	})

	t.Run("no change is a no-op", func(t *testing.T) {
		svc, mock, repo := newUserWatchTestService(t, cfg, "")
		expectWatchedUser(mock, 12, "big@example.com", 80)

		_, err := svc.SetUserBalanceWatch(context.Background(), SetUserBalanceWatchInput{UserID: 12, Threshold: 50, Note: "a"})
		require.NoError(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
		require.Equal(t, cfg, repo.values[SettingKeySecurityAlertConfig])
	})
}

func TestSetUserBalanceWatchRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		in   SetUserBalanceWatchInput
		want error
	}{
		{"zero threshold", SetUserBalanceWatchInput{UserID: 12, Threshold: 0}, ErrUserBalanceWatchThresholdInvalid},
		{"negative threshold", SetUserBalanceWatchInput{UserID: 12, Threshold: -5}, ErrUserBalanceWatchThresholdInvalid},
		{"rounds to zero", SetUserBalanceWatchInput{UserID: 12, Threshold: 0.004}, ErrUserBalanceWatchThresholdInvalid},
		{"NaN threshold", SetUserBalanceWatchInput{UserID: 12, Threshold: math.NaN()}, ErrUserBalanceWatchThresholdInvalid},
		{"too large", SetUserBalanceWatchInput{UserID: 12, Threshold: UserBalanceWatchThresholdMax + 1}, ErrUserBalanceWatchThresholdInvalid},
		{"note too long", SetUserBalanceWatchInput{UserID: 12, Threshold: 10, Note: strings.Repeat("注", UserBalanceWatchNoteMaxLength+1)}, ErrUserBalanceWatchNoteTooLong},
		{"invalid user id", SetUserBalanceWatchInput{UserID: 0, Threshold: 10}, ErrUserNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, mock, repo := newUserWatchTestService(t, `{"enabled":true}`, "")
			_, err := svc.SetUserBalanceWatch(context.Background(), tc.in)
			require.ErrorIs(t, err, tc.want)
			require.NoError(t, mock.ExpectationsWereMet())
			require.Equal(t, `{"enabled":true}`, repo.values[SettingKeySecurityAlertConfig])
		})
	}
}

func TestSetUserBalanceWatchRejectsMissingUserAndFullList(t *testing.T) {
	t.Run("missing user", func(t *testing.T) {
		svc, mock, repo := newUserWatchTestService(t, `{"enabled":true}`, "")
		mock.ExpectQuery(regexp.QuoteMeta("FROM users WHERE id = ANY")).
			WillReturnRows(sqlmock.NewRows([]string{"id", "email", "username", "balance"}))

		_, err := svc.SetUserBalanceWatch(context.Background(), SetUserBalanceWatchInput{UserID: 99, Threshold: 10})
		require.ErrorIs(t, err, ErrUserNotFound)
		require.NoError(t, mock.ExpectationsWereMet())
		require.Equal(t, `{"enabled":true}`, repo.values[SettingKeySecurityAlertConfig])
	})

	t.Run("list full", func(t *testing.T) {
		full := DefaultSecurityAlertConfig()
		for i := 1; i <= securityAlertMaxUserWatches; i++ {
			full.UserBalanceWatches = append(full.UserBalanceWatches, UserBalanceWatch{UserID: int64(1000 + i), Threshold: 1})
		}
		raw, err := json.Marshal(full)
		require.NoError(t, err)
		svc, mock, _ := newUserWatchTestService(t, string(raw), "")
		expectWatchedUser(mock, 12, "big@example.com", 8)

		_, err = svc.SetUserBalanceWatch(context.Background(), SetUserBalanceWatchInput{UserID: 12, Threshold: 10})
		require.ErrorIs(t, err, ErrUserBalanceWatchLimitReached)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestRemoveUserBalanceWatch(t *testing.T) {
	cfg := `{"enabled":true,"user_balance_watches":[{"user_id":5,"threshold":10},{"user_id":12,"threshold":50,"note":"大客户"}]}`

	t.Run("removes user and resets state", func(t *testing.T) {
		svc, mock, repo := newUserWatchTestService(t, cfg, "")
		expectUserAlertStateReset(mock, "user:12")
		expectWatchedUser(mock, 12, "big@example.com", 8.2)
		expectUserWatchNotice(mock, "已取消关注用户余额", "用户：#12 big@example.com（大客户）", "原提醒线 $50.00", "操作人：admin@example.com")

		require.NoError(t, svc.RemoveUserBalanceWatch(context.Background(), 12, "admin@example.com"))
		require.NoError(t, mock.ExpectationsWereMet())
		require.Equal(t, []UserBalanceWatch{{UserID: 5, Threshold: 10}}, savedUserWatches(t, repo))
	})

	t.Run("user not watched is a no-op", func(t *testing.T) {
		svc, mock, repo := newUserWatchTestService(t, cfg, "")
		require.NoError(t, svc.RemoveUserBalanceWatch(context.Background(), 77, "admin@example.com"))
		require.NoError(t, mock.ExpectationsWereMet())
		require.Equal(t, cfg, repo.values[SettingKeySecurityAlertConfig])
	})
}

func TestListUserBalanceWatches(t *testing.T) {
	svc, _, _ := newUserWatchTestService(t,
		`{"enabled":false,"user_balance_watches":[{"user_id":12,"threshold":50}]}`,
		"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abc")
	list, err := svc.ListUserBalanceWatches(context.Background())
	require.NoError(t, err)
	require.False(t, list.Enabled)
	require.True(t, list.WebhookConfigured)
	require.Equal(t, []UserBalanceWatch{{UserID: 12, Threshold: 50}}, list.Watches)

	empty, _, _ := newUserWatchTestService(t, "", "")
	list, err = empty.ListUserBalanceWatches(context.Background())
	require.NoError(t, err)
	require.True(t, list.Enabled)
	require.False(t, list.WebhookConfigured)
	require.NotNil(t, list.Watches)
	require.Empty(t, list.Watches)
}

func TestUserWatchNoticeIsOpsAlert(t *testing.T) {
	require.True(t, isOpsAlertKind(securityAlertKindUserWatch))
	msg := renderSecurityAlertMessage("BirdApi", []pendingSecurityAlert{{
		Kind: securityAlertKindUserWatch, Severity: SecurityAlertInfo, Title: "已关注用户余额", Body: "用户：#12 big@example.com",
	}})
	require.True(t, strings.HasPrefix(msg, "【BirdApi 运营告警】"))
	requirePlainText(t, msg)
}
