-- 邀请返利：代理测试金
-- 管理员把某个用户设为代理并给一个测试金总额度；代理可以从额度里给自己邀请的用户加余额，
-- 每笔都要写备注。额度用完不能再发，也不产生邀请返利。

ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS agent_enabled BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS agent_credit_limit DECIMAL(20,8) NOT NULL DEFAULT 0;

ALTER TABLE user_affiliates
    ADD COLUMN IF NOT EXISTS agent_credit_used DECIMAL(20,8) NOT NULL DEFAULT 0;

COMMENT ON COLUMN user_affiliates.agent_enabled IS '是否为代理（可给自己邀请的用户发测试金）';
COMMENT ON COLUMN user_affiliates.agent_credit_limit IS '代理测试金总额度（USD）';
COMMENT ON COLUMN user_affiliates.agent_credit_used IS '代理已发放的测试金合计（USD）';

CREATE TABLE IF NOT EXISTS affiliate_agent_grants (
    id BIGSERIAL PRIMARY KEY,
    agent_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    invitee_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    amount DECIMAL(20,8) NOT NULL CHECK (amount > 0),
    notes TEXT NOT NULL,
    invitee_balance_after DECIMAL(20,8) NULL,
    agent_credit_limit_after DECIMAL(20,8) NULL,
    agent_credit_used_after DECIMAL(20,8) NULL,
    operation_id VARCHAR(64) NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_affiliate_agent_grants_agent ON affiliate_agent_grants (agent_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_affiliate_agent_grants_invitee ON affiliate_agent_grants (invitee_user_id);
CREATE INDEX IF NOT EXISTS idx_affiliate_agent_grants_created_at ON affiliate_agent_grants (created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_affiliate_agent_grants_operation_id
    ON affiliate_agent_grants (operation_id)
    WHERE operation_id IS NOT NULL;

COMMENT ON TABLE affiliate_agent_grants IS '代理测试金发放记录';
COMMENT ON COLUMN affiliate_agent_grants.notes IS '代理填写的备注（必填）';
COMMENT ON COLUMN affiliate_agent_grants.operation_id IS '幂等标识（由代理 ID 与 Idempotency-Key 派生的 SHA-256 十六进制串）';
