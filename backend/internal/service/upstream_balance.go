package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

type balanceStoredConfig struct {
	OwnerIdentity string `json:"owner_identity,omitempty"`
	Mode          string `json:"mode"`
	Token         string `json:"token,omitempty"`
	UserID        string `json:"user_id,omitempty"`
}

type UpstreamBalanceConfig struct {
	Mode           string `json:"mode"`
	AccessToken    string `json:"access_token"`
	UserID         string `json:"user_id"`
	OwnerAccountID int64  `json:"owner_account_id"`
}

type UpstreamBalanceSnapshot struct {
	Data      *UpstreamBalanceData `json:"data,omitempty"`
	FetchedAt int64                `json:"fetched_at,omitempty"`
	AttemptAt int64                `json:"attempt_at,omitempty"`
	Error     string               `json:"error,omitempty"`
}

type UpstreamBalanceState struct {
	AccountID            int64                   `json:"account_id"`
	OwnerAccountID       int64                   `json:"owner_account_id"`
	Mode                 string                  `json:"mode"`
	UserID               string                  `json:"user_id,omitempty"`
	TokenConfigured      bool                    `json:"token_configured"`
	EncryptionConfigured bool                    `json:"encryption_configured"`
	Snapshot             UpstreamBalanceSnapshot `json:"snapshot"`
}

type UpstreamBalanceService struct {
	db        *sql.DB
	accounts  AccountRepository
	proxies   ProxyRepository
	http      HTTPUpstream
	encryptor SecretEncryptor
	cfg       *config.Config
	slots     chan struct{}
	reads     chan struct{}
	flights   singleflight.Group
}

func NewUpstreamBalanceService(db *sql.DB, accounts AccountRepository, proxies ProxyRepository, http HTTPUpstream, encryptor SecretEncryptor, cfg *config.Config) *UpstreamBalanceService {
	return &UpstreamBalanceService{db: db, accounts: accounts, proxies: proxies, http: http, encryptor: encryptor, cfg: cfg, slots: make(chan struct{}, 4), reads: make(chan struct{}, 4)}
}

// UpstreamBalanceHTTP is a distinct DI type so probes cannot accidentally use
// the gateway's client cache.
type UpstreamBalanceHTTP struct{ HTTPUpstream }

func ProvideUpstreamBalanceService(db *sql.DB, accounts AccountRepository, proxies ProxyRepository, http *UpstreamBalanceHTTP, encryptor SecretEncryptor, cfg *config.Config) *UpstreamBalanceService {
	return NewUpstreamBalanceService(db, accounts, proxies, http.HTTPUpstream, encryptor, cfg)
}

type balanceRowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func balanceIdentity(a *Account) string {
	sum := sha256.Sum256([]byte(a.Type + "\x00" + a.GetCredential("base_url") + "\x00" + a.GetCredential("api_key")))
	return hex.EncodeToString(sum[:])
}

// Bind sharing to the owner's credentials and query configuration, not just its
// row ID. Replacing a wallet at the same URL must require explicit relinking.
func balanceWalletIdentity(a *Account, cfg balanceStoredConfig) string {
	sum := sha256.Sum256([]byte(balanceIdentity(a) + "\x00" + cfg.Mode + "\x00" + cfg.UserID + "\x00" + cfg.Token))
	return hex.EncodeToString(sum[:])
}

func (s *UpstreamBalanceService) balanceAccount(ctx context.Context, id int64) (*Account, error) {
	a, err := s.accounts.GetByID(ctx, id)
	if err != nil || a == nil {
		return nil, errors.New("账号不存在")
	}
	if a.Type != "apikey" || strings.TrimSpace(a.GetCredential("api_key")) == "" {
		return nil, errors.New("仅支持已配置 API Key 的账号")
	}
	if _, err := upstreamBalanceRoot(a.GetCredential("base_url")); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *UpstreamBalanceService) load(ctx context.Context, a *Account) (UpstreamBalanceState, balanceStoredConfig, error) {
	return s.loadFrom(ctx, s.db, a)
}
func (s *UpstreamBalanceService) loadFrom(ctx context.Context, reader balanceRowReader, a *Account) (UpstreamBalanceState, balanceStoredConfig, error) {
	state := UpstreamBalanceState{AccountID: a.ID, OwnerAccountID: a.ID, Mode: "auto", EncryptionConfigured: s.cfg != nil && s.cfg.Totp.EncryptionKeyConfigured}
	cfg := balanceStoredConfig{Mode: "auto"}
	var identity string
	var configJSON, snapshotJSON []byte
	var owner sql.NullInt64
	err := reader.QueryRowContext(ctx, `SELECT identity, owner_account_id, config, snapshot FROM upstream_balance_accounts WHERE account_id=$1`, a.ID).Scan(&identity, &owner, &configJSON, &snapshotJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return state, cfg, nil
	}
	if err != nil {
		return state, cfg, errors.New("无法读取余额配置")
	}
	// Do not reuse a panel credential or a shared wallet after account identity changes.
	if identity != balanceIdentity(a) {
		return state, cfg, nil
	}
	if json.Unmarshal(configJSON, &cfg) != nil || json.Unmarshal(snapshotJSON, &state.Snapshot) != nil {
		return state, cfg, errors.New("余额配置损坏，请重新保存")
	}
	if cfg.Mode == "" {
		cfg.Mode = "auto"
	}
	state.Mode, state.UserID, state.TokenConfigured = cfg.Mode, cfg.UserID, cfg.Token != ""
	if owner.Valid {
		state.OwnerAccountID = owner.Int64
	}
	if cfg.Mode == "shared" && !owner.Valid {
		return state, cfg, errors.New("共享钱包源账号已删除，请重新关联")
	}
	return state, cfg, nil
}

func (s *UpstreamBalanceService) State(ctx context.Context, id int64, refresh, force bool) (UpstreamBalanceState, error) {
	// Bound the whole path, including account hydration and cached-state reads,
	// across all concurrent admin requests, not only the final upstream call.
	select {
	case s.reads <- struct{}{}:
		defer func() { <-s.reads }()
	case <-ctx.Done():
		return UpstreamBalanceState{AccountID: id, OwnerAccountID: id, Mode: "auto"}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()

	a, err := s.balanceAccount(ctx, id)
	if err != nil {
		return UpstreamBalanceState{AccountID: id, OwnerAccountID: id, Mode: "auto"}, err
	}
	state, cfg, err := s.load(ctx, a)
	if err != nil {
		return state, err
	}
	owner := a
	expectedWalletIdentity := cfg.OwnerIdentity
	if state.OwnerAccountID != id {
		owner, err = s.balanceAccount(ctx, state.OwnerAccountID)
		if err != nil {
			return state, errors.New("共享钱包源账号不可用")
		}
		root, _ := upstreamBalanceRoot(a.GetCredential("base_url"))
		ownerRoot, _ := upstreamBalanceRoot(owner.GetCredential("base_url"))
		if root != ownerRoot {
			return state, errors.New("共享钱包源账号地址已变化，请重新关联")
		}
		ownerState, ownerCfg, loadErr := s.load(ctx, owner)
		if cfg.OwnerIdentity != balanceWalletIdentity(owner, ownerCfg) {
			return state, errors.New("来源账号的余额配置已变化，请重新关联钱包")
		}
		if loadErr != nil || ownerState.OwnerAccountID != owner.ID || ownerCfg.Mode == "disabled" || ownerState.Snapshot.Data == nil || ownerState.Snapshot.Data.Scope != "account" {
			return state, errors.New("共享钱包源账号需重新查询账户余额")
		}
		state.Snapshot, cfg = ownerState.Snapshot, ownerCfg
	}
	if refresh && cfg.Mode != "disabled" {
		updated, refreshErr := s.refresh(ctx, owner, force)
		if refreshErr != nil {
			return state, refreshErr
		}
		state.Snapshot = updated
		if owner.ID != id {
			currentOwner, checkErr := s.balanceAccount(ctx, owner.ID)
			if checkErr != nil {
				state.Snapshot.Data = nil
				return state, errors.New("共享钱包来源已变化，请刷新列表")
			}
			_, currentCfg, checkErr := s.load(ctx, currentOwner)
			if checkErr != nil || balanceWalletIdentity(currentOwner, currentCfg) != expectedWalletIdentity {
				state.Snapshot.Data = nil
				return state, errors.New("共享钱包配置已变化，请重新关联")
			}
		}

		if owner.ID != id && (state.Snapshot.Data == nil || state.Snapshot.Data.Scope != "account") {
			state.Snapshot.Data = nil
			return state, errors.New("源账号只返回令牌额度，无法作为共享钱包")
		}
	}
	return state, nil
}

func (s *UpstreamBalanceService) refresh(ctx context.Context, a *Account, force bool) (UpstreamBalanceSnapshot, error) {
	result := s.flights.DoChan(strconv.FormatInt(a.ID, 10)+":"+balanceIdentity(a), func() (any, error) { return s.refreshOnce(ctx, a, force) })
	select {
	case <-ctx.Done():
		return UpstreamBalanceSnapshot{}, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return UpstreamBalanceSnapshot{}, result.Err
		}
		snapshot, ok := result.Val.(UpstreamBalanceSnapshot)
		if !ok {
			return UpstreamBalanceSnapshot{}, errors.New("余额结果无效")
		}
		return snapshot, nil
	}
}

func (s *UpstreamBalanceService) refreshOnce(ctx context.Context, a *Account, force bool) (UpstreamBalanceSnapshot, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return UpstreamBalanceSnapshot{}, ctx.Err()
	}
	// The transaction ends before any upstream or proxy I/O. Small DB pools must
	// remain usable for gateway auth/billing while a relay stalls.
	claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(claimCtx, nil)
	if err != nil {
		return UpstreamBalanceSnapshot{}, errors.New("无法开始余额查询")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(claimCtx, `SET LOCAL lock_timeout = '500ms'`); err != nil {
		return UpstreamBalanceSnapshot{}, errors.New("无法设置余额查询锁超时")
	}
	identity := balanceIdentity(a)
	_, err = tx.ExecContext(claimCtx, `INSERT INTO upstream_balance_accounts(account_id,identity) VALUES($1,$2) ON CONFLICT(account_id) DO NOTHING`, a.ID, identity)
	if err != nil {
		return UpstreamBalanceSnapshot{}, errors.New("余额查询忙，请稍后重试")
	}
	var storedIdentity string
	var rawConfig, rawSnapshot []byte
	var owner sql.NullInt64
	var leased bool
	err = tx.QueryRowContext(claimCtx, `SELECT identity,config,snapshot,owner_account_id,COALESCE(lease_until > NOW(),false) FROM upstream_balance_accounts WHERE account_id=$1 FOR UPDATE`, a.ID).Scan(&storedIdentity, &rawConfig, &rawSnapshot, &owner, &leased)
	if err != nil {
		return UpstreamBalanceSnapshot{}, errors.New("余额查询忙，请稍后重试")
	}
	cfg := balanceStoredConfig{Mode: "auto"}
	snapshot := UpstreamBalanceSnapshot{}
	if identity == storedIdentity {
		if json.Unmarshal(rawConfig, &cfg) != nil || json.Unmarshal(rawSnapshot, &snapshot) != nil {
			return snapshot, errors.New("余额配置无效")
		}
	}
	if cfg.Mode == "" {
		cfg.Mode = "auto"
	}
	if cfg.Mode == "disabled" {
		return snapshot, nil
	}
	if cfg.Mode == "shared" || owner.Valid && identity == storedIdentity {
		return snapshot, errors.New("共享钱包关联已变化，请刷新列表")
	}
	ttl := int64(300)
	if force {
		ttl = 30
	}
	if identity == storedIdentity && (leased || snapshot.AttemptAt > time.Now().Unix()-ttl) {
		return snapshot, nil
	}
	token := uuid.NewString()
	configJSON, _ := json.Marshal(cfg)
	snapshotJSON, _ := json.Marshal(snapshot)
	_, err = tx.ExecContext(claimCtx, `UPDATE upstream_balance_accounts SET identity=$2,config=$3,snapshot=$4,owner_account_id=NULL,lease_token=$5,lease_until=NOW()+INTERVAL '30 seconds' WHERE account_id=$1`, a.ID, identity, string(configJSON), string(snapshotJSON), token)
	if err != nil {
		return snapshot, errors.New("无法领取余额查询任务")
	}
	if err = tx.Commit(); err != nil {
		return snapshot, errors.New("无法提交余额查询任务")
	}
	cancel()
	completed := false
	defer func() {
		if completed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cleanupCancel()
		_, _ = s.db.ExecContext(cleanupCtx, `UPDATE upstream_balance_accounts SET lease_token='',lease_until=NULL WHERE account_id=$1 AND lease_token=$2`, a.ID, token)
	}()
	data, queryErr := s.query(ctx, a, cfg)
	snapshot.AttemptAt = time.Now().Unix()
	snapshot.Error = ""
	if queryErr != nil {
		snapshot.Error = queryErr.Error()
	} else {
		snapshot.Data = data
		snapshot.FetchedAt = snapshot.AttemptAt
	}
	snapshotJSON, err = json.Marshal(snapshot)
	if err != nil {
		return UpstreamBalanceSnapshot{}, errors.New("余额结果无效")
	}
	// Saving config invalidates the lease. Credentials are checked in the same
	// write, so a late reply cannot publish a different account's old wallet.
	result, err := s.db.ExecContext(ctx, `UPDATE upstream_balance_accounts b SET snapshot=$3,lease_token='',lease_until=NULL
 WHERE b.account_id=$1 AND b.lease_token=$2 AND EXISTS (
 SELECT 1 FROM accounts a WHERE a.id=b.account_id AND a.deleted_at IS NULL
 AND a.type=$4 AND a.credentials->>'base_url'=$5 AND a.credentials->>'api_key'=$6)`, a.ID, token, string(snapshotJSON), a.Type, a.GetCredential("base_url"), a.GetCredential("api_key"))
	if err != nil {
		return UpstreamBalanceSnapshot{}, errors.New("无法保存余额结果")
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return UpstreamBalanceSnapshot{}, errors.New("账号或余额配置已变化，请重新查询")
	}
	completed = true
	return snapshot, nil
}

func (s *UpstreamBalanceService) Save(ctx context.Context, id int64, input UpstreamBalanceConfig) (UpstreamBalanceState, error) {
	a, err := s.balanceAccount(ctx, id)
	if err != nil {
		return UpstreamBalanceState{}, err
	}
	switch input.Mode {
	case "auto", "sub2api", "new_api", "token", "disabled", "shared":
	default:
		return UpstreamBalanceState{}, errors.New("不支持的余额查询方式")
	}
	var sharedAccount *Account
	if input.Mode == "shared" {
		sharedAccount, err = s.balanceAccount(ctx, input.OwnerAccountID)
		if err != nil {
			return UpstreamBalanceState{}, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return UpstreamBalanceState{}, errors.New("无法保存余额配置")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout = '500ms'`); err != nil {
		return UpstreamBalanceState{}, errors.New("余额配置忙，请稍后重试")
	}
	// Serialize edits to the wallet relationship graph, preventing cycles.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(734291012)`); err != nil {
		return UpstreamBalanceState{}, err
	}
	// Lock existing config before loading; queries cannot overwrite a concurrent save.
	if _, err = tx.ExecContext(ctx, `INSERT INTO upstream_balance_accounts(account_id,identity) VALUES($1,$2) ON CONFLICT(account_id) DO NOTHING`, id, balanceIdentity(a)); err != nil {
		return UpstreamBalanceState{}, err
	}
	var oldIdentity string
	var raw []byte
	if err = tx.QueryRowContext(ctx, `SELECT identity,config FROM upstream_balance_accounts WHERE account_id=$1 FOR UPDATE`, id).Scan(&oldIdentity, &raw); err != nil {
		return UpstreamBalanceState{}, err
	}
	old := balanceStoredConfig{}
	if oldIdentity == balanceIdentity(a) {
		_ = json.Unmarshal(raw, &old)
	}
	cfg := balanceStoredConfig{Mode: input.Mode}
	savedSnapshot := UpstreamBalanceSnapshot{}
	var owner any
	if input.Mode == "new_api" {
		if s.cfg == nil || !s.cfg.Totp.EncryptionKeyConfigured {
			return UpstreamBalanceState{}, errors.New("请先设置固定 TOTP_ENCRYPTION_KEY，以安全保存访问令牌")
		}
		uid, parseErr := strconv.ParseInt(strings.TrimSpace(input.UserID), 10, 64)
		if parseErr != nil || uid < 1 {
			return UpstreamBalanceState{}, errors.New("请输入有效的 New API 用户 ID")
		}
		cfg.UserID = strconv.FormatInt(uid, 10)
		token := strings.TrimSpace(input.AccessToken)
		if token == "" {
			cfg.Token = old.Token
		} else {
			if len(token) > 8192 || strings.ContainsAny(token, "\r\n") {
				return UpstreamBalanceState{}, errors.New("访问令牌格式无效")
			}
			cfg.Token, err = s.encryptor.Encrypt(token)
			if err != nil {
				return UpstreamBalanceState{}, errors.New("访问令牌加密失败")
			}
		}
		if cfg.Token == "" {
			return UpstreamBalanceState{}, errors.New("请填写访问令牌（不是用于模型调用的 API Key）")
		}
	}
	if input.Mode == "shared" {
		if input.OwnerAccountID <= 0 || input.OwnerAccountID == id {
			return UpstreamBalanceState{}, errors.New("请选择另一个账号作为钱包来源")
		}
		var dependents int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM upstream_balance_accounts WHERE owner_account_id=$1`, id).Scan(&dependents); err != nil {
			return UpstreamBalanceState{}, err
		}
		if dependents > 0 {
			return UpstreamBalanceState{}, errors.New("该账号已被其他账号共享，请先解除这些关联")
		}
		other := sharedAccount
		root, _ := upstreamBalanceRoot(a.GetCredential("base_url"))
		otherRoot, _ := upstreamBalanceRoot(other.GetCredential("base_url"))
		if root != otherRoot {
			return UpstreamBalanceState{}, errors.New("仅能关联相同上游地址的钱包")
		}
		state, ownerCfg, err := s.loadFrom(ctx, tx, other)
		if err != nil || state.OwnerAccountID != other.ID || state.Mode == "disabled" || state.Snapshot.Data == nil || state.Snapshot.Data.Scope != "account" || state.Snapshot.Error != "" {
			return UpstreamBalanceState{}, errors.New("请先为来源账号成功查询账户余额；令牌额度不能共享")
		}
		owner = other.ID
		cfg.OwnerIdentity = balanceWalletIdentity(other, ownerCfg)
		savedSnapshot = state.Snapshot
	}
	raw, _ = json.Marshal(cfg)
	_, err = tx.ExecContext(ctx, `UPDATE upstream_balance_accounts SET identity=$2,owner_account_id=$3,config=$4,snapshot='{}',lease_token='',lease_until=NULL WHERE account_id=$1`, id, balanceIdentity(a), owner, string(raw))
	if err != nil {
		return UpstreamBalanceState{}, errors.New("无法保存余额配置")
	}
	if err = tx.Commit(); err != nil {
		return UpstreamBalanceState{}, errors.New("无法提交余额配置")
	}
	ownerID := id
	if input.Mode == "shared" {
		ownerID = input.OwnerAccountID
	}
	return UpstreamBalanceState{AccountID: id, OwnerAccountID: ownerID, Mode: cfg.Mode, UserID: cfg.UserID, TokenConfigured: cfg.Token != "", EncryptionConfigured: s.cfg != nil && s.cfg.Totp.EncryptionKeyConfigured, Snapshot: savedSnapshot}, nil
}

// Batch caps work independently of page size. Each item has its own error;
// one unavailable upstream must not hide the other accounts' balances.
func (s *UpstreamBalanceService) Batch(ctx context.Context, ids []int64, refresh, force bool) []UpstreamBalanceState {
	out := make([]UpstreamBalanceState, len(ids))
	var wg sync.WaitGroup
	jobs := make(chan int)
	for worker := 0; worker < min(4, len(ids)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				state, err := s.State(ctx, ids[i], refresh, force)
				if err != nil {
					state.Snapshot.Error = err.Error()
				}
				out[i] = state
			}
		}()
	}
	for i := range ids {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}
