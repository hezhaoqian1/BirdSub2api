import { apiClient } from '../client'

/** 单项检查结果：通过 / 不通过 / 无法判断（例如思考占满输出、还没有基线）。 */
export type FidelityCheckState = 'pass' | 'fail' | 'skip'
/** 一次体检的结论：pass 全部通过，fail 有检查项不通过，error 请求没成功（不算保真问题）。 */
export type FidelityStatus = 'pass' | 'fail' | 'error'

export interface ChannelFidelityConfig {
  enabled: boolean
  interval_minutes: number
  recheck_seconds: number
  models: string[]
  prompt_token_tolerance: number
}

export interface ChannelFidelityCheck {
  id: number
  account_id: number
  model: string
  upstream_model: string
  checked_at: string
  status: FidelityStatus
  is_recheck: boolean
  system_check: FidelityCheckState
  thinking_check: FidelityCheckState
  json_check: FidelityCheckState
  answer_check: FidelityCheckState
  prompt_check: FidelityCheckState
  prompt_tokens: number | null
  baseline_prompt_tokens: number | null
  completion_tokens: number | null
  reasoning_tokens: number | null
  reasoning_chars: number
  latency_ms: number | null
  http_status: number | null
  message: string
  response_excerpt: string
}

export interface ChannelFidelityPoint {
  checked_at: string
  status: FidelityStatus
  is_recheck: boolean
  message?: string
}

export interface ChannelFidelityRow {
  account_id: number
  account_name: string
  schedulable: boolean
  model: string
  upstream_model: string
  baseline_prompt_tokens: number | null
  alerting: boolean
  latest: ChannelFidelityCheck | null
  history: ChannelFidelityPoint[]
}

export interface ChannelFidelityStatusView {
  available: boolean
  config: ChannelFidelityConfig
  notify_enabled: boolean
  running: boolean
  last_sweep_at: string | null
  rows: ChannelFidelityRow[]
}

const base = '/admin/security-alerts/fidelity'

export async function getStatus() {
  return (await apiClient.get<ChannelFidelityStatusView>(`${base}/status`)).data
}

export async function updateConfig(config: Partial<ChannelFidelityConfig>) {
  return (await apiClient.put<ChannelFidelityConfig>(`${base}/config`, { config })).data
}

/** 立即在后台体检全部目标。 */
export async function runAll() {
  return (await apiClient.post<{ started: boolean }>(`${base}/run`)).data
}

/** 立即体检一个（账号，模型），同步返回结果；上游慢时最多等约 2 分钟。 */
export async function runOne(accountId: number, model: string) {
  return (await apiClient.post<ChannelFidelityCheck>(`${base}/check`, { account_id: accountId, model }, { timeout: 130000 })).data
}

/** 删除 prompt_tokens 基线，下次全部通过时重新记录。 */
export async function resetBaseline(accountId: number, upstreamModel: string) {
  return (await apiClient.post<{ reset: boolean }>(`${base}/reset-baseline`, { account_id: accountId, upstream_model: upstreamModel })).data
}
