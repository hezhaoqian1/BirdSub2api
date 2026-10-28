-- Balance alerts for upstream accounts (channels) and watched users.
--
-- security_alert_state remembers the last alerted level per subject
-- ('account:<id>', 'user:<id>') so a low balance is pushed once per crossing
-- and re-armed after a recharge. The 'sweep:*' rows coordinate periodic jobs
-- across replicas.
CREATE TABLE IF NOT EXISTS security_alert_state (
    subject    VARCHAR(100)     PRIMARY KEY,
    level      SMALLINT         NOT NULL DEFAULT 0,
    value      DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

-- Upstream balance readings taken by the background sweep; used to estimate
-- how fast each wallet is being drained. Kept for two days.
CREATE TABLE IF NOT EXISTS security_alert_balance_samples (
    account_id  BIGINT           NOT NULL,
    observed_at TIMESTAMPTZ      NOT NULL,
    remaining   DOUBLE PRECISION NOT NULL,
    unit        VARCHAR(16)      NOT NULL DEFAULT '',
    PRIMARY KEY (account_id, observed_at)
);

CREATE INDEX IF NOT EXISTS security_alert_balance_samples_observed_at_idx
    ON security_alert_balance_samples (observed_at);
