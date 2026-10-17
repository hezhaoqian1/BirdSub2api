-- Balance credentials and snapshots deliberately live outside exported account JSON.
CREATE TABLE IF NOT EXISTS upstream_balance_accounts (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    owner_account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    identity TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}',
    snapshot JSONB NOT NULL DEFAULT '{}',
    CHECK (owner_account_id IS NULL OR owner_account_id <> account_id)
);
CREATE INDEX IF NOT EXISTS idx_upstream_balance_owner ON upstream_balance_accounts(owner_account_id);
