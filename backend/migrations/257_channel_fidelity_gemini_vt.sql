-- Channel fidelity probe for Gemini "VT" (Vertex) accounts: besides DeepSeek,
-- the sweep now checks that Gemini accounts sold as Vertex really answer from
-- Vertex and never fall back to AI Studio (which drops gs:// media and video
-- input). Each Gemini probe records four checks of its own:
--   signature_check  thoughtSignature on a function call has the Vertex prefix
--   media_check      a public gs:// sample video was actually read
--   canary_check     the signature saved from the previous passing probe is
--                    still accepted (AI Studio rejects Vertex signatures)
--   meta_check       every response carries createTime and responseId
ALTER TABLE channel_fidelity_checks
    ADD COLUMN IF NOT EXISTS probe_kind       VARCHAR(16) NOT NULL DEFAULT 'deepseek',
    ADD COLUMN IF NOT EXISTS signature_check  VARCHAR(8)  NOT NULL DEFAULT 'skip',
    ADD COLUMN IF NOT EXISTS media_check      VARCHAR(8)  NOT NULL DEFAULT 'skip',
    ADD COLUMN IF NOT EXISTS canary_check     VARCHAR(8)  NOT NULL DEFAULT 'skip',
    ADD COLUMN IF NOT EXISTS meta_check       VARCHAR(8)  NOT NULL DEFAULT 'skip',
    ADD COLUMN IF NOT EXISTS signature_prefix VARCHAR(16) NOT NULL DEFAULT '';

-- Signature from the latest fully passing Gemini probe, replayed by the next
-- probe as a canary. Deleting the row (reset baseline) clears it too.
ALTER TABLE channel_fidelity_baselines
    ADD COLUMN IF NOT EXISTS canary_signature TEXT;
