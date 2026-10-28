-- 邀请返利：自定义邀请码
-- 1) user_affiliate_code_aliases: 改码后保留的历史邀请码。已分享出去的旧链接继续归属原用户，
--    历史码也不会再分配给其他用户（只有管理员可以把它改给别人）。
-- 2) aff_code_changed_at: 用户最近一次自助修改邀请码的时间，用于改码冷却期判断。

CREATE TABLE IF NOT EXISTS user_affiliate_code_aliases (
    code VARCHAR(32) PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_affiliate_code_aliases_user_id
    ON user_affiliate_code_aliases(user_id);

ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS aff_code_changed_at TIMESTAMPTZ;

COMMENT ON TABLE user_affiliate_code_aliases IS '用户历史邀请码（改码后旧码仍归属原用户）';
COMMENT ON COLUMN user_affiliate_code_aliases.code IS '历史邀请码（大写）';
COMMENT ON COLUMN user_affiliate_code_aliases.user_id IS '历史邀请码归属用户';
COMMENT ON COLUMN user_affiliates.aff_code_changed_at IS '用户最近一次自助修改邀请码的时间（NULL 表示从未自助修改）';
