import { apiClient } from '../client'

export interface UserBalanceWatch {
  user_id: number
  threshold: number
  note?: string
}

/** 只列出管理端可编辑的字段，其余安全规则字段原样保留在服务端。 */
export interface SecurityAlertConfig {
  enabled: boolean
  channel_spend_floor: number
  channel_spend_multiplier: number
  channel_balance_check_minutes: number
  channel_low_balance_usd: number
  channel_low_balance_cny: number
  channel_runway_hours: number
  channel_quota_error_notify: boolean
  user_balance_watches: UserBalanceWatch[]
  [key: string]: unknown
}

export interface UserBalanceWatchStatus {
  user_id: number
  email: string
  username: string
  balance: number
  found: boolean
  below: boolean
}

export interface SecurityAlertConfigView {
  config: SecurityAlertConfig
  wecom_webhook_configured: boolean
  wecom_webhook_masked: string
  user_watch_status: UserBalanceWatchStatus[] | null
}

const base = '/admin/security-alerts'

export async function getConfig() {
  return (await apiClient.get<SecurityAlertConfigView>(`${base}/config`)).data
}

export async function updateConfig(config: Partial<SecurityAlertConfig>) {
  return (await apiClient.put<SecurityAlertConfigView>(`${base}/config`, { config })).data
}

export async function sendTest() {
  return (await apiClient.post<{ sent: boolean }>(`${base}/test`)).data
}
