//go:build integration

package repository

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (s *UserRepoSuite) TestListByUsernameForLogin_NormalizesCaseAndSpacing() {
	user := s.mustCreateUser(&service.User{Email: "uname-login@test.com", Username: " Alice "})
	s.mustCreateUser(&service.User{Email: "uname-other@test.com", Username: "bob"})

	got, err := s.repo.ListByUsernameForLogin(s.ctx, "  ALICE ", 10)
	s.Require().NoError(err)
	s.Require().Len(got, 1)
	s.Require().Equal(user.ID, got[0].ID)
	s.Require().Equal(user.Email, got[0].Email)
	s.Require().NotEmpty(got[0].PasswordHash)
}

func (s *UserRepoSuite) TestListByUsernameForLogin_DuplicatesOrderedAndLimited() {
	first := s.mustCreateUser(&service.User{Email: "dup-1@test.com", Username: "dup"})
	second := s.mustCreateUser(&service.User{Email: "dup-2@test.com", Username: "DUP"})
	s.mustCreateUser(&service.User{Email: "dup-3@test.com", Username: "dup"})

	got, err := s.repo.ListByUsernameForLogin(s.ctx, "dup", 2)
	s.Require().NoError(err)
	s.Require().Len(got, 2)
	s.Require().Equal(first.ID, got[0].ID)
	s.Require().Equal(second.ID, got[1].ID)
}

func (s *UserRepoSuite) TestListByUsernameForLogin_BlankNeverMatchesUsersWithoutUsername() {
	s.mustCreateUser(&service.User{Email: "no-uname@test.com"})

	got, err := s.repo.ListByUsernameForLogin(s.ctx, "   ", 10)
	s.Require().NoError(err)
	s.Require().Empty(got)
}

func (s *UserRepoSuite) TestListByUsernameForLogin_SkipsDeletedUsers() {
	user := s.mustCreateUser(&service.User{Email: "deleted-uname@test.com", Username: "ghost"})
	s.Require().NoError(s.repo.Delete(s.ctx, user.ID))

	got, err := s.repo.ListByUsernameForLogin(s.ctx, "ghost", 10)
	s.Require().NoError(err)
	s.Require().Empty(got)
}
