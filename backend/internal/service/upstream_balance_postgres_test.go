//go:build integration

package service

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUpstreamBalancePostgresLeaseAndSharedWallet(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is required for the isolated PostgreSQL balance test")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	schema := "balance_test_" + uuid.NewString()[:8]
	_, err = db.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer func() { _, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE") }()
	_, err = db.ExecContext(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE TABLE accounts (id BIGINT PRIMARY KEY, type TEXT, credentials JSONB, deleted_at TIMESTAMPTZ);
 CREATE TABLE upstream_balance_accounts (account_id BIGINT PRIMARY KEY REFERENCES accounts(id), owner_account_id BIGINT REFERENCES accounts(id), identity TEXT NOT NULL, config JSONB NOT NULL DEFAULT '{}', snapshot JSONB NOT NULL DEFAULT '{}', lease_token TEXT NOT NULL DEFAULT '', lease_until TIMESTAMPTZ);
 INSERT INTO accounts VALUES (1,'apikey','{"base_url":"https://example.com","api_key":"owner-key"}',NULL),(2,'apikey','{"base_url":"https://example.com/v1","api_key":"child-key"}',NULL)`)
	require.NoError(t, err)
	owner := &Account{ID: 1, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "owner-key"}}
	child := &Account{ID: 2, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com/v1", "api_key": "child-key"}}
	upstream := &balanceHTTPTestStub{bodies: map[string]string{"/v1/usage": `{"isValid":true,"balance":12.5}`}}
	svc := NewUpstreamBalanceService(db, &balanceAccountMapTestRepo{accounts: map[int64]*Account{1: owner, 2: child}}, nil, upstream, nil, nil)
	state, err := svc.State(ctx, 1, true, false)
	require.NoError(t, err)
	require.Empty(t, state.Snapshot.Error)
	require.Equal(t, 12.5, *state.Snapshot.Data.Remaining)
	var leased bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT lease_token <> '' OR lease_until IS NOT NULL FROM upstream_balance_accounts WHERE account_id=1").Scan(&leased))
	require.False(t, leased)
	shared, err := svc.Save(ctx, 2, UpstreamBalanceConfig{Mode: "shared", OwnerAccountID: 1})
	require.NoError(t, err)
	require.Equal(t, int64(1), shared.OwnerAccountID)
	state, err = svc.State(ctx, 2, true, true)
	require.NoError(t, err)
	require.Equal(t, 12.5, *state.Snapshot.Data.Remaining)
	require.Len(t, upstream.requests, 1, "shared refresh must honor the owner's cooldown")
	_, err = svc.Save(ctx, 1, UpstreamBalanceConfig{Mode: "disabled"})
	require.NoError(t, err)
	_, err = svc.State(ctx, 2, false, false)
	require.Error(t, err, "changing source configuration must invalidate the shared wallet")
}
