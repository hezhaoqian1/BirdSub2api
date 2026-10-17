import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useUpstreamBalances } from '../useUpstreamBalances'
import type { UpstreamBalanceState } from '@/api/admin/upstreamBalances'

const { query } = vi.hoisted(() => ({ query: vi.fn() }))
vi.mock('@/api/admin/upstreamBalances', () => ({ queryUpstreamBalances: query }))

function snapshot(id: number, owner = id): UpstreamBalanceState {
  return { account_id: id, owner_account_id: owner, mode: owner === id ? 'auto' : 'shared',
    token_configured: false, encryption_configured: true,
    snapshot: { attempt_at: Math.floor(Date.now() / 1000), data: { scope: 'account', remaining: 10, unit: 'USD', source: 'sub2api' } } }
}
beforeEach(() => vi.resetAllMocks())
afterEach(() => vi.restoreAllMocks())

describe('balance list queries', () => {
  it('does not start background queries in a hidden tab', async () => {
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden')
    const wrapper = mount(defineComponent({ setup() {
      useUpstreamBalances(ref([{ id: 1, type: 'apikey', credentials: { base_url: 'https://example.com' } }]))
      return () => null
    } }))
    await flushPromises()
    expect(query).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('aborts queued work when hidden and ignores late results', async () => {
    const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible')
    let balances!: ReturnType<typeof useUpstreamBalances>
    query.mockResolvedValueOnce([snapshot(1)])
    const wrapper = mount(defineComponent({ setup() {
      balances = useUpstreamBalances(ref([{ id: 1, type: 'apikey', credentials: { base_url: 'https://example.com' } }]))
      return () => null
    } }))
    await flushPromises()
    let resolveRefresh!: (states: UpstreamBalanceState[]) => void
    query.mockImplementationOnce(() => new Promise(resolve => { resolveRefresh = resolve }))
    const refresh = balances.refreshBalances([1])
    visibility.mockReturnValue('hidden')
    document.dispatchEvent(new Event('visibilitychange'))
    expect(query.mock.calls[1][3].aborted).toBe(true)
    const late = snapshot(1); late.snapshot.data!.remaining = 99
    resolveRefresh([late])
    await refresh
    expect(balances.balanceStates.value[1].snapshot.data?.remaining).toBe(10)
    wrapper.unmount()
    await balances.refreshBalances([1])
    expect(query).toHaveBeenCalledTimes(2)
  })

  it('clears the previous wallet immediately when the API key changes', async () => {
    let balances!: ReturnType<typeof useUpstreamBalances>
    const rows = ref([{ id: 1, type: 'apikey', credentials: { base_url: 'https://example.com', api_key: 'old-key' } }])
    query.mockResolvedValueOnce([snapshot(1)])
    const wrapper = mount(defineComponent({ setup() { balances = useUpstreamBalances(rows); return () => null } }))
    await flushPromises()
    let resolveOld!: (states: UpstreamBalanceState[]) => void
    let resolveNew!: (states: UpstreamBalanceState[]) => void
    query.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const oldRefresh = balances.refreshBalances([1])
    query.mockImplementationOnce(() => new Promise(resolve => { resolveNew = resolve }))
    rows.value[0].credentials.api_key = 'new-key'
    await flushPromises()
    expect(query.mock.calls[1][3].aborted).toBe(true)
    expect(balances.balanceStates.value[1]).toBeUndefined()
    resolveOld([snapshot(1)])
    await oldRefresh
    expect(balances.balanceStates.value[1]).toBeUndefined()
    const updated = snapshot(1); updated.snapshot.data!.remaining = 25
    resolveNew([updated])
    await flushPromises()
    expect(balances.balanceStates.value[1].snapshot.data?.remaining).toBe(25)
    wrapper.unmount()
  })

  it('refreshes all visible wallet siblings and leaves unrelated accounts cached', async () => {
    let balances!: ReturnType<typeof useUpstreamBalances>
    const rows = ref([1, 2, 3].map(id => ({ id, type: 'apikey', credentials: { base_url: 'https://example.com' } })))
    query.mockResolvedValueOnce([snapshot(1), snapshot(2, 1), snapshot(3)])
    const wrapper = mount(defineComponent({ setup() { balances = useUpstreamBalances(rows); return () => null } }))
    await flushPromises()
    query.mockResolvedValueOnce([snapshot(1), snapshot(2, 1)])
    await balances.refreshBalances([2])
    expect(query.mock.calls[1][0].sort()).toEqual([1, 2])
    expect(query.mock.calls[1][1]).toBe(true)
    wrapper.unmount()
  })

  it('does not erase successful balances when the batch request fails', async () => {
    let balances!: ReturnType<typeof useUpstreamBalances>
    query.mockResolvedValueOnce([snapshot(1)])
    const wrapper = mount(defineComponent({ setup() {
      balances = useUpstreamBalances(ref([{ id: 1, type: 'apikey', credentials: { base_url: 'https://example.com' } }]))
      return () => null
    } }))
    await flushPromises()
    query.mockRejectedValueOnce(new Error('network unavailable'))
    await balances.refreshBalances([1])
    expect(balances.balanceStates.value[1].snapshot.data?.remaining).toBe(10)
    expect(balances.balanceError.value).toBe('upstreamBalance.requestFailed')
    expect(balances.balanceBusy.value[1]).toBe(false)
    wrapper.unmount()
  })
})
