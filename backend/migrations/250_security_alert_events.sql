-- Security alert outbox: every replica inserts detected events (deduplicated by
-- dedupe_key), and the leader instance delivers pending rows to the configured
-- WeCom robot with retry/backoff.
CREATE TABLE IF NOT EXISTS security_alert_events (
    id              BIGSERIAL PRIMARY KEY,
    dedupe_key      VARCHAR(200) NOT NULL,
    kind            VARCHAR(64)  NOT NULL,
    severity        VARCHAR(16)  NOT NULL DEFAULT 'warning',
    title           VARCHAR(200) NOT NULL,
    body            TEXT         NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    sent_at         TIMESTAMPTZ,
    attempts        INTEGER      NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    locked_at       TIMESTAMPTZ,
    locked_by       VARCHAR(64)  NOT NULL DEFAULT '',
    last_error      TEXT         NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS security_alert_events_dedupe_key_uidx
    ON security_alert_events (dedupe_key);

CREATE INDEX IF NOT EXISTS security_alert_events_pending_idx
    ON security_alert_events (next_attempt_at, id)
    WHERE sent_at IS NULL;

CREATE INDEX IF NOT EXISTS security_alert_events_created_at_idx
    ON security_alert_events (created_at);
