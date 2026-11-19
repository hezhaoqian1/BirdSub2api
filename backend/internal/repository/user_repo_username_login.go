package repository

import (
	"context"
	"strings"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/predicate"
	dbuser "github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/service"

	entsql "entgo.io/ent/dialect/sql"
)

var _ service.UsernameLoginRepository = (*userRepository)(nil)

// ListByUsernameForLogin 按 LOWER(TRIM(username)) 精确匹配未删除的用户（fork：用户名登录）。
// username 为空时直接返回空，避免把所有没设用户名的用户都匹配出来。
func (r *userRepository) ListByUsernameForLogin(ctx context.Context, username string, limit int) ([]*service.User, error) {
	normalized := strings.ToLower(strings.TrimSpace(username))
	if normalized == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	rows, err := r.client.User.Query().
		Where(userUsernameLookupPredicate(normalized)).
		Order(dbent.Asc(dbuser.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	ids := make([]int64, 0, len(rows))
	for _, m := range rows {
		ids = append(ids, m.ID)
	}
	groups, err := r.loadAllowedGroups(ctx, ids)
	if err != nil {
		return nil, err
	}

	out := make([]*service.User, 0, len(rows))
	for _, m := range rows {
		u := userEntityToService(m)
		if v, ok := groups[m.ID]; ok {
			u.AllowedGroups = v
		}
		out = append(out, u)
	}
	return out, nil
}

func userUsernameLookupPredicate(normalized string) predicate.User {
	return predicate.User(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString("LOWER(TRIM(").
				Ident(s.C(dbuser.FieldUsername)).
				WriteString(")) = ").
				Arg(normalized)
		}))
	})
}
