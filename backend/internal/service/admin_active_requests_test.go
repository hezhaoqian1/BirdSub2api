//go:build unit

package service

import (
	"context"
	"database/sql"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/stretchr/testify/require"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

func newActiveRequestsTestClient(t *testing.T) *dbent.Client {
	t.Helper()
	db, err := sql.Open("sqlite", "file:admin_active_requests?mode=memory&cache=shared&_fk=1")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestDescribeActiveSlotsResolvesNames(t *testing.T) {
	client := newActiveRequestsTestClient(t)
	ctx := context.Background()

	owner, err := client.User.Create().SetEmail("alice@example.com").SetUsername("alice").SetPasswordHash("x").Save(ctx)
	require.NoError(t, err)
	grp, err := client.Group.Create().SetName("Claude-Pro").SetPlatform(PlatformAnthropic).Save(ctx)
	require.NoError(t, err)
	key, err := client.APIKey.Create().SetUserID(owner.ID).SetKey("sk-test-active").SetName("cc-main").Save(ctx)
	require.NoError(t, err)
	acc, err := client.Account.Create().SetName("zzone-03").SetPlatform(PlatformAnthropic).SetType(AccountTypeAPIKey).SetCredentials(map[string]any{}).Save(ctx)
	require.NoError(t, err)

	svc := &adminServiceImpl{entClient: client}
	views, err := svc.DescribeActiveSlots(ctx, &ActiveSlotSnapshot{Now: 1000, Slots: []ActiveSlot{
		{SlotID: "s1", AccountID: acc.ID, StartedAt: 959, Holder: &SlotHolder{RequestID: "r1", UserID: owner.ID, APIKeyID: key.ID, GroupID: grp.ID, Model: "claude-opus-5"}},
		// 详情只记了 Key：用户从 Key 反查。
		{SlotID: "s2", StartedAt: 990, Holder: &SlotHolder{APIKeyID: key.ID}},
		{SlotID: "s3", StartedAt: 1001},
		{SlotID: "live", Live: true, AccountID: 999999, StartedAt: 900},
	}})
	require.NoError(t, err)
	require.Len(t, views, 4)

	require.Equal(t, ActiveRequestView{
		SlotID: "s1", Recorded: true, RequestID: "r1", ElapsedSeconds: 41, Model: "claude-opus-5",
		UserID: owner.ID, UserEmail: "alice@example.com", Username: "alice",
		APIKeyID: key.ID, APIKeyName: "cc-main",
		GroupID: grp.ID, GroupName: "Claude-Pro", GroupPlatform: PlatformAnthropic,
		AccountID: acc.ID, AccountName: "zzone-03", AccountPlatform: PlatformAnthropic,
	}, views[0])
	require.Equal(t, owner.ID, views[1].UserID)
	require.Equal(t, "alice@example.com", views[1].UserEmail)
	require.False(t, views[2].Recorded)
	require.Zero(t, views[2].ElapsedSeconds, "clock skew must not produce negative durations")
	require.True(t, views[3].Live)
	require.Zero(t, views[3].ElapsedSeconds, "live lease scores are refresh times, not start times")
	require.Empty(t, views[3].AccountName, "unknown ids keep only the id")
}

func TestDescribeActiveSlotsEmpty(t *testing.T) {
	svc := &adminServiceImpl{}
	views, err := svc.DescribeActiveSlots(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, views)
	require.Empty(t, views)
}
