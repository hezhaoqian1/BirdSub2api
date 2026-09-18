CREATE TABLE IF NOT EXISTS channel_monitor_hybrid_config (
    id integer PRIMARY KEY CHECK (id = 1),
    version integer NOT NULL DEFAULT 1,
    config jsonb NOT NULL DEFAULT '{"enabled":false,"rules":[]}',
    webhook_encrypted text NOT NULL DEFAULT '',
    secret_encrypted text NOT NULL DEFAULT ''
);
INSERT INTO channel_monitor_hybrid_config(id) VALUES (1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS channel_monitor_hybrid_minutes (
    rule_id text NOT NULL,
    minute timestamptz NOT NULL,
    result jsonb NOT NULL,
    PRIMARY KEY(rule_id, minute)
);
CREATE TABLE IF NOT EXISTS channel_monitor_hybrid_states (
    rule_id text PRIMARY KEY,
    state jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS channel_monitor_hybrid_cursors (
    rule_id text PRIMARY KEY,
    next_minute timestamptz NOT NULL,
    last_probe_at timestamptz,
    probe_lease_until timestamptz
);
CREATE TABLE IF NOT EXISTS channel_monitor_hybrid_notifications (
    id bigserial PRIMARY KEY,
    rule_id text NOT NULL,
    minute timestamptz NOT NULL,
    kind text NOT NULL,
    message text NOT NULL,
    generation integer NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT NOW(),
    sent_at timestamptz,
    cancelled_at timestamptz,
    locked_at timestamptz,
    locked_by text NOT NULL DEFAULT '',
    last_error text NOT NULL DEFAULT '',
    UNIQUE(rule_id, minute, kind)
);
CREATE TABLE IF NOT EXISTS channel_monitor_hybrid_runtime (
    id integer PRIMARY KEY CHECK (id = 1),
    heartbeat_at timestamptz NOT NULL DEFAULT NOW(),
    last_error text NOT NULL DEFAULT '',
    blind_alerted boolean NOT NULL DEFAULT false
);
INSERT INTO channel_monitor_hybrid_runtime(id) VALUES (1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS channel_monitor_hybrid_activity (
    instance_id text NOT NULL,
    group_id bigint NOT NULL,
    in_flight bigint NOT NULL DEFAULT 0,
    last_activity_at timestamptz,
    heartbeat_at timestamptz NOT NULL DEFAULT NOW(),
    PRIMARY KEY(instance_id, group_id)
);
CREATE INDEX IF NOT EXISTS channel_monitor_hybrid_minutes_time ON channel_monitor_hybrid_minutes(minute);
CREATE INDEX IF NOT EXISTS channel_monitor_hybrid_notifications_pending
    ON channel_monitor_hybrid_notifications(next_attempt_at, id)
    WHERE sent_at IS NULL AND cancelled_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_usage_logs_hybrid_minute
    ON usage_logs(group_id, (COALESCE(NULLIF(BTRIM(requested_model), ''), model)), created_at, api_key_id);
CREATE INDEX IF NOT EXISTS idx_ops_error_logs_hybrid_minute
    ON ops_error_logs(group_id, (COALESCE(NULLIF(BTRIM(requested_model), ''), model)), created_at, api_key_id)
    WHERE NOT is_count_tokens;
