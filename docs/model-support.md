# 模型接入说明

## 2026-09-30 同步来源

- 上游主线：`Wei-Shaw/sub2api` 的 `a60a29549`，版本 `v0.2.10`。
- Claude Sonnet 5.5：上游已合并的 PR #7683，功能提交 `8490a8186` 和 lint 修正 `2cc459e24`。
- GPT-6.1 Sol：PR #7730 中独立的模型提交 `9688571a8`。该 PR 当时尚未合并；本次不引入其订阅档位和 Astra Ultrafast 提交。
- 历史接入参考：PR #7509 的 GPT-6 Sol/Luna、Claude Opus 5.5 实现。

## 接入链路

新模型不只是增加一个下拉选项，需要一起检查以下层次：

1. **模型发现**：`backend/internal/pkg/openai/constants.go`、`backend/internal/pkg/claude/constants.go` 提供默认模型目录；分组白名单和账号模型映射仍限制可见性。
2. **别名与路由**：`openai_model_alias.go`、`openai_codex_transform.go`、`openai_compat_model.go` 处理供应商前缀、推理后缀及 compact 名称。参数校验应使用最终映射后的模型。
3. **协议兼容**：`backend/internal/pkg/apicompat/` 和各 `openai_gateway_*` / `gateway_*` 转发路径处理 Chat Completions、Responses、Messages 的参数和消息转换。
4. **Codex 能力清单**：`openai_codex_models_service.go` 和 `openai_codex_model_metadata.go` 提供上下文、推理等级、工具及速度信息；账号上游返回的明确值优先于离线默认值。
5. **计费**：同时更新资源价格快照、`pricing_service.go`、`billing_service.go`，测试缓存写入/读取、服务档位、长上下文边界及自定义零价覆盖。
6. **前端**：`useModelWhitelist.ts`、`UseKeyModal.vue`，以及 Claude 账号状态模型列表。

## GPT-6.1 Sol

- 模型 ID：`gpt-6.1-sol`，独立于 `gpt-6-sol`；原有 `gpt-6` → Astra 映射不变。
- 原生 API 推理等级为 `low`、`medium`、`high`、`xhigh`、`max`。兼容接口拒绝 `none`、`minimal` 和显式关闭思考，避免静默升级请求。
- 工具调用使用 Responses；仅支持 Chat Completions 的账号收到工具请求时返回明确错误，不丢弃工具后继续转发。
- Codex 离线描述来自 `openai/codex@b1e72963c3b71a9265a551e54beff078384efed9`。默认推理等级为 `low`，上下文窗口为 272K / 872K；这些值与 API 上下文上限分别维护。
- Codex `ultra` 是客户端多代理能力，不是额外的原生 API 推理等级。
- 标准价格（美元/百万 token）：输入 2、输出 10、缓存读取 0.10、缓存写入 2.50；Fast 为标准价格的 2 倍，Flex 为 0.5 倍。
- 输入总量超过 272,000 token 时，整笔请求的输入及缓存价格乘 2，输出价格乘 1.5；边界计数包含缓存 token。

## Claude Sonnet 5.5

- 模型 ID：`claude-sonnet-5-5`；上游实现也处理 OpenRouter 点号别名及 Bedrock 前缀。
- 默认 adaptive thinking；`between_tools` 限于 low / medium / high，不能附带手工预算等字段。
- 沿用上游对强制工具选择、采样参数、签名 thinking 历史、重试、toolset beta 和 Bedrock Global 的适配。
- 标准价格（美元/百万 token）：输入 2、输出 10、缓存读取 0.20、5 分钟缓存写入 2.50、1 小时缓存写入 4。

## 验证与边界

```powershell
cd backend
go test -tags=unit ./internal/pkg/openai ./internal/pkg/claude ./internal/pkg/apicompat ./internal/service ./internal/handler -run 'Test.*(GPT61|GPT6Sol|Sonnet55|NewModelPricing)' -count=1
go test -tags=unit -p 2 ./...
cd ../frontend
npx --yes pnpm@9.15.9 install --frozen-lockfile
npx --yes pnpm@9.15.9 test:run
npx --yes pnpm@9.15.9 typecheck
npx --yes pnpm@9.15.9 lint:check
npx --yes pnpm@9.15.9 build
```

模型接入不修改现有默认模型、不自动开放分组白名单，也不代表每个订阅账号都已获得模型权限。单元测试使用本地 fixture / mock；真实账号可用性需另行测试。

本次验证：前端 336 个测试文件 / 2532 项测试、类型检查、lint、生产构建，以及含前端资源的 Go `embed` 构建通过。模型定向测试覆盖 GPT-6.1 Sol、Sonnet 5.5、计费边界和现有混合监控。

Windows 全量 Go 测试需要让当前终端的 PATH 包含 Git for Windows 的 `usr/bin`，供备份测试调用 `sh`；并发构建内存不足时使用 `-p 1`。解决依赖下载和运行环境问题后，仍有上游原有的 `TestOllamaProbeCallback_StaleLongDoesNotOverrideNewShort` 失败，单独重跑也复现。该测试及对应 Ollama 限流实现与同步的上游一致，本次未修改；不能把全量后端测试标记为全部通过。

官方参考：

- https://developers.openai.com/api/docs/models/gpt-6.1-sol
- https://developers.openai.com/api/docs/pricing
- https://platform.claude.com/docs/en/models/sonnet-5-5/whats-new-sonnet-5-5
