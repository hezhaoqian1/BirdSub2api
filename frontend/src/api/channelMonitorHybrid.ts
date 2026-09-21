import { apiClient } from './client'

export interface HybridRule {
  id: string
  name: string
  model: string
  group_id: number
  monitor_id: number
  enabled: boolean
  public: boolean
  muted: boolean
  red_success: number
  green_success: number
  warning_ttft_ms: number
  critical_ttft_ms: number
  minimum_ttft_samples: number
}

export interface HybridMinute {
  minute: string
  observed_at: string
  source: string
  status: string
  reasons: string[]
  success_rate: number | null
  alert_success_rate?: number
  requests?: number
  successes?: number
  failures?: number
  excluded?: number
  ttft_ms: number | null
  ttft_samples?: number
  ttft_sufficient: boolean
  detail?: string
  finalized: boolean
}

export interface HybridItem {
  rule: HybridRule
  minutes: HybridMinute[]
  state?: { minute: string; red_count: number; green_count: number; incident: boolean; generation: number; blind_count: number; blind_spot: boolean }
}

export interface HybridConfig {
  version: number
  enabled: boolean
  notifications_enabled: boolean
  rules: HybridRule[]
  webhook?: string
  secret?: string
  clear_credentials?: boolean
  webhook_configured: boolean
  secret_configured: boolean
}

export interface HybridNotification {
  id: number
  rule_id: string
  minute: string
  kind: string
  attempts: number
  sent_at: string | null
  last_error: string
}

export interface HybridSnapshot {
  config: HybridConfig
  items: HybridItem[]
  notifications?: HybridNotification[]
}

export async function getHybridSnapshot(admin = false): Promise<HybridSnapshot> {
  const { data } = await apiClient.get<HybridSnapshot>(`${admin ? '/admin' : ''}/channel-monitor-hybrid`)
  return data
}

export async function saveHybridConfig(config: HybridConfig): Promise<HybridConfig> {
  const { data } = await apiClient.put<HybridConfig>('/admin/channel-monitor-hybrid/config', config)
  return data
}

export async function testHybridNotification(): Promise<void> {
  await apiClient.post('/admin/channel-monitor-hybrid/test-notification')
}
