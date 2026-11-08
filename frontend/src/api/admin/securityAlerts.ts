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
  /** 按最近扣费速度预计在这么多分钟内用完时再提醒一次，0 关闭。 */
  channel_urgent_minutes?: number
  /** 同一钱包 5 分钟内被上游以余额不足拒绝达到该次数时立即提醒，0 关闭。 */
  channel_reject_threshold?: number
  /** 余额告警过的渠道检测到充值时推送一条。 */
  channel_recharge_notify?: boolean
  channel_quota_error_notify: boolean
  channel_monitor_notify: boolean
  /** 渠道保真体检复测确认不通过时推送，恢复后再推一次。 */
  channel_fidelity_notify: boolean
  /** 用户在线充值成功时每笔推送。 */
  recharge_notify: boolean
  /** 充值订单过期没付成且金额 ≥ 该值（元）时推送，0 关闭。 */
  recharge_expired_notify_cny: number
  /** 已付款但超过该分钟数未到账时告警，0 关闭。 */
  recharge_stuck_minutes: number
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
