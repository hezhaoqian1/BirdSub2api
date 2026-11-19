package service

import (
	"context"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// 用户名登录（fork 功能）：密码登录时，输入框既可以填邮箱也可以填用户名。
// 先按邮箱查找；邮箱查无此人时再按用户名兜底。没有设置用户名的用户不受影响。

// usernameLoginMaxCandidates 同名候选账号的上限。users.username 不唯一，
// 每个候选都要做一次 bcrypt 校验，所以需要封顶。
const usernameLoginMaxCandidates = 10

// usernameLoginMaxRunes 与 users.username 的 MaxLen(100) 保持一致。
const usernameLoginMaxRunes = 100

// ErrUsernameAmbiguous 同一个用户名下有多个账号且密码都对得上，无法确定登录哪一个。
// 只有在密码正确时才会返回，不会泄露用户名是否存在。
var ErrUsernameAmbiguous = infraerrors.Conflict(
	"USERNAME_AMBIGUOUS",
	"该用户名对应多个账号，请使用邮箱登录 / This username matches multiple accounts, please sign in with your email",
)

// UsernameLoginRepository 是生产用户仓储为用户名登录提供的可选能力。
// 独立于 UserRepository，避免所有测试桩都要实现它。
type UsernameLoginRepository interface {
	// ListByUsernameForLogin 按 LOWER(TRIM(username)) 精确匹配未删除的用户，按 ID 升序，最多 limit 个。
	ListByUsernameForLogin(ctx context.Context, username string, limit int) ([]*User, error)
}

func normalizeLoginUsername(identifier string) string {
	return strings.ToLower(strings.TrimSpace(identifier))
}

// findLoginUserByUsername 按用户名查找密码匹配的账号。
// 用户名不唯一：逐个校验密码，恰好一个账号匹配时才放行。
func (s *AuthService) findLoginUserByUsername(ctx context.Context, identifier, password string) (*User, error) {
	username := normalizeLoginUsername(identifier)
	if username == "" || len([]rune(username)) > usernameLoginMaxRunes {
		return nil, ErrInvalidCredentials
	}
	repo, ok := s.userRepo.(UsernameLoginRepository)
	if !ok {
		return nil, ErrInvalidCredentials
	}
	candidates, err := repo.ListByUsernameForLogin(ctx, username, usernameLoginMaxCandidates)
	if err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Database error during username login: %v", err)
		return nil, ErrServiceUnavailable
	}

	var matched *User
	for _, candidate := range candidates {
		if candidate == nil || !s.CheckPassword(password, candidate.PasswordHash) {
			continue
		}
		if matched != nil {
			return nil, ErrUsernameAmbiguous
		}
		matched = candidate
	}
	if matched == nil {
		return nil, ErrInvalidCredentials
	}
	return matched, nil
}
