-- Channel fidelity probe ("保真体检"): every N minutes the security-alert leader
-- sends one tiny request to each upstream DeepSeek account with a system
-- passphrase, thinking disabled and JSON mode, and records whether the upstream
-- honored them. Kept for seven days.
CREATE TABLE IF NOT EXISTS channel_fidelity_checks (
    id                     BIGSERIAL    PRIMARY KEY,
    account_id             BIGINT       NOT NULL,
    model                  VARCHAR(100) NOT NULL,
    upstream_model         VARCHAR(100) NOT NULL,
    checked_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    status                 VARCHAR(16)  NOT NULL,
    is_recheck             BOOLEAN      NOT NULL DEFAULT FALSE,
    system_check           VARCHAR(8)   NOT NULL DEFAULT 'skip',
    thinking_check         VARCHAR(8)   NOT NULL DEFAULT 'skip',
    json_check             VARCHAR(8)   NOT NULL DEFAULT 'skip',
    answer_check           VARCHAR(8)   NOT NULL DEFAULT 'skip',
    prompt_check           VARCHAR(8)   NOT NULL DEFAULT 'skip',
    prompt_tokens          INTEGER,
    baseline_prompt_tokens INTEGER,
    completion_tokens      INTEGER,
    reasoning_tokens       INTEGER,
    reasoning_chars        INTEGER      NOT NULL DEFAULT 0,
    latency_ms             INTEGER,
    http_status            INTEGER,
    message                TEXT         NOT NULL DEFAULT '',
    response_excerpt       TEXT         NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS channel_fidelity_checks_target_idx
    ON channel_fidelity_checks (account_id, upstream_model, checked_at DESC);

CREATE INDEX IF NOT EXISTS channel_fidelity_checks_checked_at_idx
    ON channel_fidelity_checks (checked_at);

-- prompt_tokens of the first fully passing probe per (account, upstream model).
-- The probe prompt has a fixed token length, so a later drift means the upstream
-- injected or dropped prompt text. Deleting a row re-learns it.
CREATE TABLE IF NOT EXISTS channel_fidelity_baselines (
    account_id     BIGINT       NOT NULL,
    upstream_model VARCHAR(100) NOT NULL,
    prompt_tokens  INTEGER      NOT NULL,
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (account_id, upstream_model)
);
