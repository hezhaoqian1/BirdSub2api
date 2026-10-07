/**
 * Admin profit statistics API
 * 收入 = 用户实际扣费，成本 = 官方价 × 请求时账号成本倍率，毛利 = 收入 − 成本
 */

import { apiClient } from '../client'

export interface ProfitAmounts {
  requests: number
  tokens: number
  standard_cost: number
  revenue: number
  cost: number
  profit: number
  profit_margin: number | null
}

export interface ProfitDailyRow extends ProfitAmounts {
  date: string
}

export interface ProfitGroupRow extends ProfitAmounts {
  group_id: number
  group_name: string
}

export interface ProfitModelRow extends ProfitAmounts {
  model: string
}

export interface ProfitUserRow extends ProfitAmounts {
  user_id: number
  email: string
}

export type ProfitCostStatus = 'configured' | 'unconfigured' | 'subscription'

export interface ProfitAccountRow extends ProfitAmounts {
  account_id: number
  account_name: string
  platform: string
  type: string
  cost_rate: number
  rate_sync_enabled?: boolean
  probe_rate?: number
  probe_status?: string
  cost_status: ProfitCostStatus
  deleted?: boolean
}

export interface ProfitCostBackfillState {
  applied_at: string
  start_time: string
  end_time: string
  account_ids: number[]
  rows: number
}

export interface ProfitReport {
  start_time: string
  end_time: string
  exclude_admin: boolean
  summary: ProfitAmounts
  subscription: ProfitAmounts
  unconfigured_revenue: number
  daily: ProfitDailyRow[]
  by_group: ProfitGroupRow[]
  by_account: ProfitAccountRow[]
  by_model: ProfitModelRow[]
  by_user: ProfitUserRow[]
  backfill?: ProfitCostBackfillState
}

export interface ProfitReportParams {
  start_date: string
  end_date: string
  exclude_admin: boolean
  group_id?: number
}

export interface ProfitCostBackfillCandidate {
  account_id: number
  account_name: string
  platform: string
  rate: number
  rows: number
  old_cost: number
  new_cost: number
}

export interface ProfitCostBackfillPreview {
  start_time: string
  end_time: string
  applied?: ProfitCostBackfillState
  candidates: ProfitCostBackfillCandidate[]
}

export async function getReport(params: ProfitReportParams): Promise<ProfitReport> {
  const { data } = await apiClient.get<ProfitReport>('/admin/profit/report', { params })
  return data
}

export async function previewCostBackfill(startDate: string): Promise<ProfitCostBackfillPreview> {
  const { data } = await apiClient.get<ProfitCostBackfillPreview>('/admin/profit/cost-backfill/preview', {
    params: { start_date: startDate }
  })
  return data
}

export async function applyCostBackfill(
  startDate: string,
  accountIds: number[],
  timezone: string
): Promise<ProfitCostBackfillState> {
  const { data } = await apiClient.post<ProfitCostBackfillState>('/admin/profit/cost-backfill', {
    start_date: startDate,
    timezone,
    account_ids: accountIds
  })
  return data
}

export const profitAPI = {
  getReport,
  previewCostBackfill,
  applyCostBackfill
}

export default profitAPI
