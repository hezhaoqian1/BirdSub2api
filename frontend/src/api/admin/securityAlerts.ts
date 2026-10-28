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

export interface UserBalanceWatchList {
  /** 告警推送总开关；关闭时关注用户也不会推送。 */
  enabled: boolean
  wecom_webhook_configured: boolean
  watches: UserBalanceWatch[]
}

/** 用户管理页：关注余额的用户名单。 */
export async function listUserWatches() {
  return (await apiClient.get<UserBalanceWatchList>(`${base}/user-watches`)).data
}

/** 新增或修改单个用户的余额提醒线，只改这一个用户。 */
export async function setUserWatch(userId: number, payload: { threshold: number; note?: string }) {
  return (await apiClient.put<UserBalanceWatch>(`${base}/user-watches/${userId}`, payload)).data
}

/** 取消关注单个用户的余额。 */
export async function removeUserWatch(userId: number) {
  return (await apiClient.delete<{ user_id: number }>(`${base}/user-watches/${userId}`)).data
}
