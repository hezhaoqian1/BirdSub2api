//go:build unit

package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

type usernameLoginRepoStub struct {
	*userRepoStub
	byUsername    map[string][]*User
	listErr       error
	listCalls     int
	lastLookupArg string
}

func (s *usernameLoginRepoStub) ListByUsernameForLogin(_ context.Context, username string, limit int) ([]*User, error) {
	s.listCalls++
	s.lastLookupArg = username
	if s.listErr != nil {
		return nil, s.listErr
	}
	users := s.byUsername[strings.ToLower(strings.TrimSpace(username))]
	if len(users) > limit {
		users = users[:limit]
	}
	return users, nil
}

func newUsernameLoginTestService(repo UserRepository) *AuthService {
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "test-secret", ExpireHour: 1}}
	return NewAuthService(nil, repo, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil)
}

func mustHashPassword(t *testing.T, password string) string {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return string(hash)
}

func newLoginTestUser(t *testing.T, id int64, email, username, password string) *User {
	t.Helper()
	return &User{
		ID:           id,
		Email:        email,
		Username:     username,
		PasswordHash: mustHashPassword(t, password),
		Role:         RoleUser,
		Status:       StatusActive,
	}
}

func TestAuthServiceLogin_ByUsername(t *testing.T) {
	alice := newLoginTestUser(t, 7, "alice@example.com", "Alice", "secret-1")
	repo := &usernameLoginRepoStub{
		userRepoStub: &userRepoStub{usersByEmail: map[string]*User{alice.Email: alice}},
		byUsername:   map[string][]*User{"alice": {alice}},
	}
	svc := newUsernameLoginTestService(repo)

	token, user, err := svc.Login(context.Background(), "  ALICE ", "secret-1")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Equal(t, int64(7), user.ID)
	require.Equal(t, 1, repo.listCalls)
}

func TestAuthServiceLogin_ByUsernameWrongPassword(t *testing.T) {
	alice := newLoginTestUser(t, 7, "alice@example.com", "alice", "secret-1")
	repo := &usernameLoginRepoStub{
		userRepoStub: &userRepoStub{},
		byUsername:   map[string][]*User{"alice": {alice}},
	}
	svc := newUsernameLoginTestService(repo)

	_, _, err := svc.Login(context.Background(), "alice", "wrong")
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestAuthServiceLogin_UnknownUsername(t *testing.T) {
	repo := &usernameLoginRepoStub{userRepoStub: &userRepoStub{}}
	svc := newUsernameLoginTestService(repo)

	_, _, err := svc.Login(context.Background(), "nobody", "secret-1")
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestAuthServiceLogin_EmailStillWorksAndSkipsUsernameLookup(t *testing.T) {
	alice := newLoginTestUser(t, 7, "alice@example.com", "", "secret-1")
	repo := &usernameLoginRepoStub{
		userRepoStub: &userRepoStub{usersByEmail: map[string]*User{alice.Email: alice}},
	}
	svc := newUsernameLoginTestService(repo)

	_, user, err := svc.Login(context.Background(), "alice@example.com", "secret-1")
	require.NoError(t, err)
	require.Equal(t, int64(7), user.ID)
	require.Zero(t, repo.listCalls)

	// 邮箱命中但密码错误时不再去按用户名兜底。
	_, _, err = svc.Login(context.Background(), "alice@example.com", "wrong")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.Zero(t, repo.listCalls)
}

func TestAuthServiceLogin_DuplicateUsernamePicksPasswordMatch(t *testing.T) {
	first := newLoginTestUser(t, 3, "first@example.com", "bob", "pw-first")
	second := newLoginTestUser(t, 9, "second@example.com", "Bob", "pw-second")
	repo := &usernameLoginRepoStub{
		userRepoStub: &userRepoStub{},
		byUsername:   map[string][]*User{"bob": {first, second}},
	}
	svc := newUsernameLoginTestService(repo)

	_, user, err := svc.Login(context.Background(), "bob", "pw-second")
	require.NoError(t, err)
	require.Equal(t, int64(9), user.ID)

	_, user, err = svc.Login(context.Background(), "bob", "pw-first")
	require.NoError(t, err)
	require.Equal(t, int64(3), user.ID)
}

func TestAuthServiceLogin_DuplicateUsernameSamePasswordIsAmbiguous(t *testing.T) {
	first := newLoginTestUser(t, 3, "first@example.com", "bob", "same-pw")
	second := newLoginTestUser(t, 9, "second@example.com", "bob", "same-pw")
	repo := &usernameLoginRepoStub{
		userRepoStub: &userRepoStub{},
		byUsername:   map[string][]*User{"bob": {first, second}},
	}
	svc := newUsernameLoginTestService(repo)

	_, _, err := svc.Login(context.Background(), "bob", "same-pw")
	require.ErrorIs(t, err, ErrUsernameAmbiguous)
}

func TestAuthServiceLogin_UsernameDisabledUser(t *testing.T) {
	alice := newLoginTestUser(t, 7, "alice@example.com", "alice", "secret-1")
	alice.Status = StatusDisabled
	repo := &usernameLoginRepoStub{
		userRepoStub: &userRepoStub{},
		byUsername:   map[string][]*User{"alice": {alice}},
	}
	svc := newUsernameLoginTestService(repo)

	_, _, err := svc.Login(context.Background(), "alice", "secret-1")
	require.ErrorIs(t, err, ErrUserNotActive)
}

func TestAuthServiceLogin_UsernameLookupErrorIsServiceUnavailable(t *testing.T) {
	repo := &usernameLoginRepoStub{userRepoStub: &userRepoStub{}, listErr: errors.New("db down")}
	svc := newUsernameLoginTestService(repo)

	_, _, err := svc.Login(context.Background(), "alice", "secret-1")
	require.ErrorIs(t, err, ErrServiceUnavailable)
}

func TestAuthServiceLogin_RepoWithoutUsernameSupport(t *testing.T) {
	svc := newUsernameLoginTestService(&userRepoStub{})

	_, _, err := svc.Login(context.Background(), "alice", "secret-1")
	require.ErrorIs(t, err, ErrInvalidCredentials)
}

func TestAuthServiceLogin_BlankOrOversizedUsernameSkipsLookup(t *testing.T) {
	repo := &usernameLoginRepoStub{userRepoStub: &userRepoStub{}}
	svc := newUsernameLoginTestService(repo)

	_, _, err := svc.Login(context.Background(), "   ", "secret-1")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	_, _, err = svc.Login(context.Background(), strings.Repeat("x", 101), "secret-1")
	require.ErrorIs(t, err, ErrInvalidCredentials)
	require.Zero(t, repo.listCalls)
}
