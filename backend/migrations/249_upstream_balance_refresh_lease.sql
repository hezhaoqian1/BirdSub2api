-- A short claim replaces holding a database transaction during upstream I/O.
ALTER TABLE upstream_balance_accounts
    ADD COLUMN IF NOT EXISTS lease_token TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;
