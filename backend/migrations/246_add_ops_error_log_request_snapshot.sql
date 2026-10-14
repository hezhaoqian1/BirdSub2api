-- 报错请求的客户端输入快照。
--
-- 与 136 移除的重放字段 request_body 不同：这里只保存有界的 JSON 快照
-- （请求体首尾各 32KB + 结构化摘要），仅在用户可见的报错且开启
-- ops 高级设置 record_request_body_on_error 时写入，用于排查与手动重放。
-- 可空列、无默认值：在 PostgreSQL 11+ 上为仅元数据变更，不重写表。
ALTER TABLE ops_error_logs
  ADD COLUMN IF NOT EXISTS request_snapshot TEXT;
