-- Gemini VT passive detection: every gateway replica counts, per Gemini API-key
-- account / upstream model / 5-minute bucket, how many real customer responses
-- looked non-Vertex (no responseId, a thoughtSignature not starting with "AY",
-- or image/video input that the response did not count). Replicas add their
-- counts with UPSERT once a minute; the security-alert leader reads the last
-- few buckets to detect an upstream mixing in or falling back to AI Studio and
-- pauses the account. Kept for seven days.
CREATE TABLE IF NOT EXISTS gemini_upstream_fingerprints (
    account_id       BIGINT       NOT NULL,
    model            VARCHAR(100) NOT NULL,
    bucket_start     TIMESTAMPTZ  NOT NULL,
    responses        INTEGER      NOT NULL DEFAULT 0,
    non_vertex       INTEGER      NOT NULL DEFAULT 0,
    no_response_id   INTEGER      NOT NULL DEFAULT 0,
    no_create_time   INTEGER      NOT NULL DEFAULT 0,
    vertex_signature INTEGER      NOT NULL DEFAULT 0,
    other_signature  INTEGER      NOT NULL DEFAULT 0,
    media_requests   INTEGER      NOT NULL DEFAULT 0,
    media_dropped    INTEGER      NOT NULL DEFAULT 0,
    PRIMARY KEY (account_id, model, bucket_start)
);

CREATE INDEX IF NOT EXISTS gemini_upstream_fingerprints_bucket_idx
    ON gemini_upstream_fingerprints (bucket_start);
