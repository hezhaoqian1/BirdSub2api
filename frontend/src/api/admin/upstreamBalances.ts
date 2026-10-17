import { apiClient } from '../client'

export type BalanceMode = 'auto' | 'sub2api' | 'new_api' | 'token' | 'shared' | 'disabled'
export interface UpstreamBalanceState {
  account_id: number
  owner_account_id: number
  mode: BalanceMode
  user_id?: string
  token_configured: boolean
  encryption_configured: boolean
  snapshot: {
    fetched_at?: number
    attempt_at?: number
    error?: string
    data?: {
      scope: 'account' | 'token' | 'subscription'
      remaining?: number
      used?: number
      unit: string
      unlimited?: boolean
      source: string
    }
  }
}
export interface UpstreamBalanceConfig {
  mode: BalanceMode
  access_token?: string
  user_id?: string
  owner_account_id?: number
}

export async function queryUpstreamBalances(account_ids: number[], refresh = false, force = false, signal?: AbortSignal) {
  const { data } = await apiClient.post<UpstreamBalanceState[]>('/admin/accounts/upstream-balances/query',
    { account_ids, refresh, force }, { timeout: 120000, signal })
  return data
}

export async function saveUpstreamBalanceConfig(id: number, config: UpstreamBalanceConfig) {
  const { data } = await apiClient.put<UpstreamBalanceState>(`/admin/accounts/${id}/upstream-balance`, config)
  return data
}
