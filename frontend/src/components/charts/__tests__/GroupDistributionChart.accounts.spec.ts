import { beforeEach, describe, it, expect, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import GroupDistributionChart from '../GroupDistributionChart.vue'
import AccountDistributionChart from '../AccountDistributionChart.vue'

const { getUserBreakdown, getAccountStats } = vi.hoisted(() => ({ getUserBreakdown: vi.fn(), getAccountStats: vi.fn() }))
vi.mock('@/api/admin/dashboard', () => ({ getUserBreakdown, getAccountStats }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-chartjs', () => ({ Doughnut: { props: ['data', 'options'], template: '<div />' } }))

const props = {
  groupStats: [
    { group_id: 5, group_name: 'Group A', requests: 10, total_tokens: 100, cost: 20, actual_cost: 10, account_cost: 4 },
    { group_id: 6, group_name: 'Group B', requests: 2, total_tokens: 50, cost: 10, actual_cost: 5, account_cost: 2 },
  ],
  enableAccountBreakdown: true,
  startDate: '2026-10-01', endDate: '2026-10-02',
  filters: { user_id: 8, model: 'test-model', native_compaction_v2: true },
}
const row = (wrapper: ReturnType<typeof mount>, text: string) => wrapper.findAll('tr').find(r => r.text().includes(text) && r.classes().includes('cursor-pointer'))!

describe('group account breakdown', () => {
  beforeEach(() => {
    getUserBreakdown.mockReset().mockResolvedValue({ users: [] })
    getAccountStats.mockReset().mockResolvedValue({ accounts: [] })
  })
  it('preserves user expansion and scopes account costs to the clicked group and current filters', async () => {
    const wrapper = mount(GroupDistributionChart, { props })
    await row(wrapper, 'Group A').trigger('click')
    await flushPromises()
    expect(getUserBreakdown).toHaveBeenCalledWith(expect.objectContaining({ group_id: 5 }))
    expect(getAccountStats).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="group-breakdown-account"]').trigger('click')
    await flushPromises()
    expect(getAccountStats).toHaveBeenCalledWith({ group_id: 5, user_id: 8, model: 'test-model', native_compaction_v2: true, start_date: '2026-10-01', end_date: '2026-10-02' })
    expect(wrapper.findComponent(AccountDistributionChart).props('metric')).toBe('account_cost')
    await wrapper.get('[data-testid="group-breakdown-user"]').trigger('click')
    await flushPromises()
    expect(getUserBreakdown).toHaveBeenCalledTimes(2)
  })
  it('ignores a stale response after selecting another group and closes on filter changes', async () => {
    let resolveOld!: (value: unknown) => void
    getAccountStats.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const wrapper = mount(GroupDistributionChart, { props })
    await row(wrapper, 'Group A').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="group-breakdown-account"]').trigger('click')
    await row(wrapper, 'Group B').trigger('click')
    await flushPromises()
    resolveOld({ accounts: [{ account_id: 99, account_name: 'STALE' }] })
    await flushPromises()
    expect(wrapper.findComponent(AccountDistributionChart).props('accountStats')).toEqual([])
    expect(getAccountStats).toHaveBeenLastCalledWith(expect.objectContaining({ group_id: 6 }))
    await wrapper.setProps({ startDate: '2026-10-03' })
    expect(wrapper.findComponent(AccountDistributionChart).exists()).toBe(false)
  })
  it('does not expose the admin account option unless enabled', async () => {
    const wrapper = mount(GroupDistributionChart, { props: { ...props, enableAccountBreakdown: false } })
    await row(wrapper, 'Group A').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="group-breakdown-account"]').exists()).toBe(false)
    expect(getAccountStats).not.toHaveBeenCalled()
  })
})
