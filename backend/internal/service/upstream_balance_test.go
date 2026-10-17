package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUpstreamBalanceSub2APIScopes(t *testing.T) {
	tests := []struct {
		name, body, scope string
		remaining         float64
		unlimited, fail   bool
	}{
		{name: "wallet", body: `{"isValid":true,"mode":"unrestricted","balance":174.50187902,"remaining":174.50187902,"unit":"USD"}`, scope: "account", remaining: 174.50187902},
		{name: "zero wallet", body: `{"isValid":true,"balance":0}`, scope: "account"},
		{name: "negative wallet", body: `{"isValid":true,"balance":-1.2}`, scope: "account", remaining: -1.2},
		{name: "limited key", body: `{"isValid":true,"mode":"quota_limited","quota":{"limit":10,"used":3,"remaining":7}}`, scope: "token", remaining: 7},
		{name: "overdrawn key", body: `{"isValid":true,"mode":"quota_limited","quota":{"limit":10,"remaining":-1}}`, scope: "token", remaining: -1},
		{name: "subscription", body: `{"isValid":true,"mode":"unrestricted","subscription":{"daily_limit_usd":10},"remaining":4}`, scope: "subscription", remaining: 4},
		{name: "unlimited subscription", body: `{"isValid":true,"subscription":{},"remaining":-1}`, scope: "subscription", unlimited: true},
		{name: "ambiguous remaining", body: `{"isValid":true,"remaining":100000000}`, fail: true},
		{name: "explicit invalid", body: `{"isValid":false,"balance":10}`, fail: true},
		{name: "missing numbers", body: `{"isValid":true,"balance":null}`, fail: true},
		{name: "html SPA", body: `<!doctype html><title>New API</title>`, fail: true},
		{name: "nonfinite", body: `{"isValid":true,"balance":"NaN"}`, fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := parseSub2APIBalance([]byte(tt.body))
			if tt.fail {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.scope, d.Scope)
			require.Equal(t, tt.unlimited, d.Unlimited)
			if tt.unlimited {
				require.Nil(t, d.Remaining)
			} else {
				require.NotNil(t, d.Remaining)
				require.InDelta(t, tt.remaining, *d.Remaining, 1e-8)
			}
		})
	}
}

func TestUpstreamBalanceNewAPINeverConfusesUnlimitedTokenWithWallet(t *testing.T) {
	status := []byte(`{"success":true,"data":{"quota_per_unit":1000,"quota_display_type":"USD"}}`)
	unlimited := []byte(`{"success":true,"data":{"total_granted":0,"total_used":2017726,"total_available":-2017726,"unlimited_quota":true}}`)
	d, err := parseNewAPIBalance(unlimited, status, false)
	require.NoError(t, err)
	require.Equal(t, "token", d.Scope)
	require.True(t, d.Unlimited)
	require.Nil(t, d.Remaining)
	require.InDelta(t, 2017.726, *d.Used, 1e-7)
	d, err = parseNewAPIBalance([]byte(`{"success":true,"data":{"unlimited_quota":true,"user_quota":{"total_available":2500,"total_used":500}}}`), status, false)
	require.NoError(t, err)
	require.Equal(t, "account", d.Scope)
	require.False(t, d.Unlimited)
	require.Equal(t, 2.5, *d.Remaining)
	d, err = parseNewAPIBalance([]byte(`{"success":true,"data":{"quota":0,"used_quota":500}}`), nil, true)
	require.NoError(t, err)
	require.Equal(t, "QUOTA", d.Unit)
	require.Equal(t, 0.0, *d.Remaining)
	_, err = parseNewAPIBalance([]byte(`{"success":false,"message":"secret-from-upstream"}`), status, true)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-from-upstream")
	_, err = parseNewAPIBalance([]byte(`{"success":true,"data":{}}`), status, true)
	require.Error(t, err)
	_, err = parseNewAPIBalance([]byte(`{"success":true,"data":{"quota":100}}`), []byte(`{"data":{"quota_per_unit":1e-309,"quota_display_type":"USD"}}`), true)
	require.Error(t, err)
}

func TestUpstreamBalanceNewAPIBooleanCodeEnvelope(t *testing.T) {
	// Sanitized shape captured from ZZONE's token usage endpoint.
	body := []byte(`{"code":true,"message":"","data":{"object":"token_usage","total_available":-123,"total_used":123,"unlimited_quota":true}}`)
	d, err := parseNewAPIBalance(body, nil, false)
	require.NoError(t, err)
	require.Equal(t, "token", d.Scope)
	require.True(t, d.Unlimited)
	require.Nil(t, d.Remaining)
	for _, envelope := range []string{`"code":false`, `"code":0`, `"code":"true"`, `"success":false,"code":true`} {
		_, err := parseNewAPIBalance([]byte(`{`+envelope+`,"data":{"unlimited_quota":true}}`), nil, false)
		require.Error(t, err, envelope)
	}
}

type balanceHTTPTestStub struct {
	HTTPUpstream
	requests []*http.Request
	bodies   map[string]string
	before   func(*http.Request)
}

func (f *balanceHTTPTestStub) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if f.before != nil {
		f.before(r)
	}
	f.requests = append(f.requests, r)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(f.bodies[r.URL.Path]))}, nil
}

type balanceAccountTestRepo struct {
	AccountRepository
	account *Account
}

func (r *balanceAccountTestRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}

func TestUpstreamBalanceRequestsUseCorrectCredentialsAndBlockRedirects(t *testing.T) {
	httpStub := &balanceHTTPTestStub{bodies: map[string]string{"/api/user/self": `{"success":true,"data":{"quota":500000,"used_quota":0}}`, "/api/status": `{"data":{"quota_per_unit":500000,"quota_display_type":"USD"}}`}}
	s := NewUpstreamBalanceService(nil, nil, nil, httpStub, ollamaUsageTestEncryptor{}, nil)
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com/v1/", "api_key": "inference-only"}}
	_, err := s.query(context.Background(), a, balanceStoredConfig{Mode: "new_api", Token: "cipher:panel-secret", UserID: "12"})
	require.NoError(t, err)
	require.Len(t, httpStub.requests, 2)
	require.Equal(t, "Bearer panel-secret", httpStub.requests[0].Header.Get("Authorization"))
	require.Equal(t, "12", httpStub.requests[0].Header.Get("New-Api-User"))
	require.Empty(t, httpStub.requests[1].Header.Get("Authorization"))
	require.True(t, HTTPUpstreamRedirectsDisabled(httpStub.requests[0].Context()))
	for _, raw := range []string{"https://example.com?x=1", "https://user:password@example.com", "file:///tmp/test", "https://example.com/#fragment"} {
		_, err := upstreamBalanceRoot(raw)
		require.Error(t, err)
	}
}

func TestUpstreamBalanceFailurePreservesLastGoodAndPersistsCooldown(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "test-key"}}
	snapshot := `{"data":{"scope":"account","remaining":10,"unit":"USD","source":"sub2api"},"fetched_at":123,"attempt_at":123}`
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO upstream_balance_accounts").WithArgs(int64(7), balanceIdentity(a)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT identity,config,snapshot,owner_account_id").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"identity", "config", "snapshot", "owner", "leased"}).AddRow(balanceIdentity(a), `{"mode":"sub2api"}`, snapshot, nil, false))
	mock.ExpectExec("UPDATE upstream_balance_accounts").WithArgs(int64(7), balanceIdentity(a), `{"mode":"sub2api"}`, sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec("UPDATE upstream_balance_accounts b SET snapshot").WithArgs(int64(7), sqlmock.AnyArg(), sqlmock.AnyArg(), "apikey", "https://example.com", "test-key").WillReturnResult(sqlmock.NewResult(0, 1))
	s := NewUpstreamBalanceService(db, &balanceAccountTestRepo{account: a}, nil, &balanceHTTPTestStub{bodies: map[string]string{}}, nil, nil)
	result, err := s.refresh(context.Background(), a, true)
	require.NoError(t, err)
	require.Equal(t, 10.0, *result.Data.Remaining)
	require.Equal(t, int64(123), result.FetchedAt)
	require.NotEmpty(t, result.Error)
	require.GreaterOrEqual(t, result.AttemptAt, time.Now().Unix()-1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamBalanceChangedIdentityDoesNotExposeOldStateOrSecrets(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "new-key"}}
	mock.ExpectQuery("SELECT identity, owner_account_id, config, snapshot").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"identity", "owner", "config", "snapshot"}).AddRow("old-identity", nil, `{"mode":"new_api","token":"encrypted-secret"}`, `{"data":{"scope":"account","remaining":50}}`))
	s := NewUpstreamBalanceService(db, nil, nil, nil, nil, &config.Config{})
	state, cfg, err := s.load(context.Background(), a)
	require.NoError(t, err)
	require.Equal(t, "auto", state.Mode)
	require.Empty(t, cfg.Token)
	require.Nil(t, state.Snapshot.Data)
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamBalanceSaveEncryptsTokenAndReturnsOnlyConfiguredFlag(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "inference-key"}}
	cfg := &config.Config{}
	cfg.Totp.EncryptionKeyConfigured = true
	s := NewUpstreamBalanceService(db, &balanceAccountTestRepo{account: a}, nil, nil, ollamaUsageTestEncryptor{}, cfg)
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO upstream_balance_accounts").WithArgs(int64(7), balanceIdentity(a)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT identity,config").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"identity", "config"}).AddRow(balanceIdentity(a), `{}`))
	stored := `{"mode":"new_api","token":"cipher:panel-secret","user_id":"12"}`
	mock.ExpectExec("UPDATE upstream_balance_accounts").WithArgs(int64(7), balanceIdentity(a), nil, stored).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := s.Save(context.Background(), 7, UpstreamBalanceConfig{Mode: "new_api", AccessToken: "panel-secret", UserID: "12"})
	require.NoError(t, err)
	require.True(t, result.TokenConfigured)
	require.Equal(t, "12", result.UserID)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "panel-secret")
	require.NotContains(t, string(raw), "cipher:")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamBalanceCooldownAvoidsNetwork(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "key"}}
	recent, _ := json.Marshal(UpstreamBalanceSnapshot{AttemptAt: time.Now().Unix(), Error: "previous failure"})
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO upstream_balance_accounts").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT identity,config,snapshot,owner_account_id").WillReturnRows(sqlmock.NewRows([]string{"identity", "config", "snapshot", "owner", "leased"}).AddRow(balanceIdentity(a), `{"mode":"auto"}`, string(recent), nil, false))
	mock.ExpectRollback()
	// A nil transport would panic if the persisted cooldown were ignored.
	s := NewUpstreamBalanceService(db, nil, nil, nil, nil, nil)
	snapshot, err := s.refresh(context.Background(), a, true)
	require.NoError(t, err)
	require.Equal(t, "previous failure", snapshot.Error)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamBalanceWalletIdentityChangesWithCredentialsOrPanelConfiguration(t *testing.T) {
	a := &Account{Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "key"}}
	cfg := balanceStoredConfig{Mode: "new_api", UserID: "12", Token: "encrypted-panel-token"}
	original := balanceWalletIdentity(a, cfg)
	cfg.UserID = "13"
	require.NotEqual(t, original, balanceWalletIdentity(a, cfg))
	cfg.UserID = "12"
	a.Credentials["api_key"] = "other-key"
	require.NotEqual(t, original, balanceWalletIdentity(a, cfg))
}

func expectBalanceClaim(mock sqlmock.Sqlmock, a *Account) {
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO upstream_balance_accounts").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT identity,config,snapshot,owner_account_id").WillReturnRows(sqlmock.NewRows([]string{"identity", "config", "snapshot", "owner", "leased"}).AddRow(balanceIdentity(a), `{"mode":"sub2api"}`, `{}`, nil, false))
	mock.ExpectExec("UPDATE upstream_balance_accounts SET identity").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
}

func TestUpstreamBalanceReleasesSingleDatabaseConnectionBeforeNetwork(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "key"}}
	expectBalanceClaim(mock, a)
	// Simulate a gateway DB read while the upstream call is in progress. If
	// refresh still holds its transaction, this times out with a one-slot pool.
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"one"}).AddRow(1))
	mock.ExpectExec("UPDATE upstream_balance_accounts b SET snapshot").WillReturnResult(sqlmock.NewResult(0, 1))
	httpStub := &balanceHTTPTestStub{bodies: map[string]string{"/v1/usage": `{"isValid":true,"balance":10}`}}
	httpStub.before = func(r *http.Request) {
		require.Zero(t, db.Stats().InUse, "upstream wait must hold no DB connections")
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		var one int
		require.NoError(t, db.QueryRowContext(ctx, "SELECT 1").Scan(&one))
		require.Equal(t, 1, one)
	}
	s := NewUpstreamBalanceService(db, nil, nil, httpStub, nil, nil)
	result, err := s.refreshOnce(t.Context(), a, true)
	require.NoError(t, err)
	require.Equal(t, 10.0, *result.Data.Remaining)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamBalanceLateReplyCannotOverwriteChangedConfig(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "key"}}
	expectBalanceClaim(mock, a)
	mock.ExpectExec("UPDATE upstream_balance_accounts b SET snapshot").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE upstream_balance_accounts SET lease_token").WillReturnResult(sqlmock.NewResult(0, 0))
	s := NewUpstreamBalanceService(db, nil, nil, &balanceHTTPTestStub{bodies: map[string]string{"/v1/usage": `{"isValid":true,"balance":999}`}}, nil, nil)
	result, err := s.refreshOnce(t.Context(), a, true)
	require.ErrorContains(t, err, "已变化")
	require.Nil(t, result.Data)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamBalanceActiveReplicaLeaseAvoidsDuplicateNetworkCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "key"}}
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO upstream_balance_accounts").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT identity,config,snapshot,owner_account_id").WillReturnRows(sqlmock.NewRows([]string{"identity", "config", "snapshot", "owner", "leased"}).AddRow(balanceIdentity(a), `{"mode":"auto"}`, `{}`, nil, true))
	mock.ExpectRollback()
	s := NewUpstreamBalanceService(db, nil, nil, nil, nil, nil)
	_, err = s.refreshOnce(t.Context(), a, true)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

type slowBalanceReadRepo struct {
	AccountRepository
	active atomic.Int64
	peak   atomic.Int64
	calls  atomic.Int64
}

func (r *slowBalanceReadRepo) GetByID(ctx context.Context, _ int64) (*Account, error) {
	r.calls.Add(1)
	n := r.active.Add(1)
	defer r.active.Add(-1)
	for {
		old := r.peak.Load()
		if old >= n || r.peak.CompareAndSwap(old, n) {
			break
		}
	}
	select {
	case <-time.After(2 * time.Millisecond):
	case <-ctx.Done():
	}
	return nil, errors.New("not found")
}

func TestUpstreamBalanceBoundsDatabaseReadsAcrossConcurrentBatches(t *testing.T) {
	repo := &slowBalanceReadRepo{}
	s := NewUpstreamBalanceService(nil, repo, nil, nil, nil, nil)
	ids := make([]int64, 100)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = s.Batch(t.Context(), ids, false, false) }()
	}
	wg.Wait()
	require.Equal(t, int64(400), repo.calls.Load())
	require.Equal(t, int64(4), repo.peak.Load())
	require.Zero(t, repo.active.Load())
}

type balanceAccountMapTestRepo struct {
	AccountRepository
	accounts map[int64]*Account
}

func (r *balanceAccountMapTestRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	return r.accounts[id], nil
}

func TestUpstreamBalanceSaveSharedWalletDoesNotRequireSecondDatabaseConnection(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com/v1", "api_key": "child-key"}}
	owner := &Account{ID: 8, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com", "api_key": "wallet-key"}}
	mock.ExpectBegin()
	mock.ExpectExec("SET LOCAL lock_timeout").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO upstream_balance_accounts").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT identity,config").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"identity", "config"}).AddRow(balanceIdentity(a), `{}`))
	mock.ExpectQuery("SELECT count").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT identity, owner_account_id, config, snapshot").WithArgs(int64(8)).WillReturnRows(sqlmock.NewRows([]string{"identity", "owner", "config", "snapshot"}).AddRow(balanceIdentity(owner), nil, `{"mode":"auto"}`, `{"data":{"scope":"account","remaining":10,"unit":"USD"}}`))
	mock.ExpectExec("UPDATE upstream_balance_accounts").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	s := NewUpstreamBalanceService(db, &balanceAccountMapTestRepo{accounts: map[int64]*Account{7: a, 8: owner}}, nil, nil, nil, nil)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	state, err := s.Save(ctx, 7, UpstreamBalanceConfig{Mode: "shared", OwnerAccountID: 8})
	require.NoError(t, err)
	require.Equal(t, int64(8), state.OwnerAccountID)
	require.Equal(t, 10.0, *state.Snapshot.Data.Remaining)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamBalanceTokenEndpointPreservesTrailingSlash(t *testing.T) {
	for _, allowlist := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Security.URLAllowlist.Enabled = allowlist
		cfg.Security.URLAllowlist.UpstreamHosts = []string{"example.com"}
		httpStub := &balanceHTTPTestStub{bodies: map[string]string{
			"/api/usage/token/": `{"code":true,"data":{"unlimited_quota":true,"total_available":-10}}`,
			"/api/status":       `{"data":{"quota_per_unit":500000,"quota_display_type":"USD"}}`,
		}}
		s := NewUpstreamBalanceService(nil, nil, nil, httpStub, nil, cfg)
		a := &Account{ID: 7, Type: "apikey", Credentials: map[string]any{"base_url": "https://example.com/v1/", "api_key": "test-key"}}
		data, err := s.query(t.Context(), a, balanceStoredConfig{Mode: "token"})
		require.NoError(t, err, "allowlist enabled: %t", allowlist)
		require.Equal(t, "token", data.Scope)
		require.True(t, data.Unlimited)
		require.Nil(t, data.Remaining)
		require.Len(t, httpStub.requests, 2)
		require.Equal(t, "https://example.com/api/usage/token/", httpStub.requests[0].URL.String())
		require.Equal(t, "Bearer test-key", httpStub.requests[0].Header.Get("Authorization"))
		require.True(t, HTTPUpstreamRedirectsDisabled(httpStub.requests[0].Context()))
		require.Empty(t, httpStub.requests[1].Header.Get("Authorization"))
		cfg.Security.URLAllowlist.Enabled = true
		cfg.Security.URLAllowlist.UpstreamHosts = []string{"other.example.com"}
		_, err = s.query(t.Context(), a, balanceStoredConfig{Mode: "token"})
		require.Error(t, err)
		require.Len(t, httpStub.requests, 2, "rejected target must not send credentials")
	}
}
