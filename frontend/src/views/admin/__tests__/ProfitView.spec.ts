import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { ProfitAccountRow, ProfitAmounts, ProfitReport } from '@/api/admin/profit'
import ProfitView from '../ProfitView.vue'

const { getReport, previewCostBackfill, applyCostBackfill, updateAccount, showError, showSuccess } = vi.hoisted(() => ({
  getReport: vi.fn(),
  previewCostBackfill: vi.fn(),
  applyCostBackfill: vi.fn(),
  updateAccount: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('vue-chartjs', () => ({
  Line: { name: 'Line', props: ['data', 'options'], template: '<div class="line-chart" />' }
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    profit: { getReport, previewCostBackfill, applyCostBackfill },
    accounts: { update: updateAccount }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const amounts = (revenue: number, cost: number): ProfitAmounts => ({
  requests: 10,
  tokens: 1000,
  standard_cost: cost * 2,
  revenue,
  cost,
  profit: revenue - cost,
  profit_margin: revenue > 0 ? (revenue - cost) / revenue : null
})

const account = (overrides: Partial<ProfitAccountRow>): ProfitAccountRow => ({
  account_id: 1,
  account_name: 'sixoner',
  platform: 'openai',
  type: 'apikey',
  cost_rate: 1,
  cost_status: 'unconfigured',
  ...amounts(30, 20),
  ...overrides
})

const createReport = (): ProfitReport => ({
  start_time: '2026-10-01T00:00:00+08:00',
  end_time: '2026-10-08T00:00:00+08:00',
  exclude_admin: true,
  summary: amounts(1290, 903),
  subscription: amounts(463, 0),
  unconfigured_revenue: 30,
  daily: [{ date: '2026-10-01', ...amounts(100, 60) }],
  by_group: [{ group_id: 7, group_name: 'GPT 独享分组', ...amounts(555, 600) }],
  by_account: [
    account({ account_id: 1, probe_rate: 0.25, probe_status: 'ok' }),
    account({ account_id: 2, account_name: 'oauth', type: 'oauth', cost_status: 'subscription' })
  ],
  by_model: [],
  by_user: []
})

const mountView = () =>
  mount(ProfitView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: true,
        DateRangePicker: true,
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }
      }
    }
  })

describe('ProfitView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getReport.mockResolvedValue(createReport())
    updateAccount.mockResolvedValue({})
  })

  it('loads the report excluding admin usage by default and shows totals', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getReport).toHaveBeenCalledWith(expect.objectContaining({ exclude_admin: true }))
    expect(wrapper.find('[data-testid="profit-revenue"]').text()).toBe('$1,290.00')
    expect(wrapper.find('[data-testid="profit-profit"]').text()).toBe('$387.00')
    expect(wrapper.find('[data-testid="profit-unconfigured-warning"]').exists()).toBe(true)
    // 亏损分组标红
    expect(wrapper.find('td.text-red-600').exists()).toBe(true)
  })

  it('reloads with admin usage included when the toggle is switched off', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('[data-testid="profit-exclude-admin"]').trigger('click')
    await flushPromises()

    expect(getReport).toHaveBeenLastCalledWith(expect.objectContaining({ exclude_admin: false }))
  })

  it('fills the probed rate and saves it as the account cost rate', async () => {
    const wrapper = mountView()
    await flushPromises()

    const accountTab = wrapper.findAll('button').find((b) => b.text() === 'admin.profit.tabs.account')!
    await accountTab.trigger('click')

    expect(wrapper.find('[data-testid="profit-rate-input-2"]').exists()).toBe(false)

    await wrapper.findAll('button').find((b) => b.text() === 'admin.profit.useProbeRate')!.trigger('click')
    expect((wrapper.find('[data-testid="profit-rate-input-1"]').element as HTMLInputElement).value).toBe('0.25')

    await wrapper.findAll('button').find((b) => b.text() === 'admin.profit.saveRate')!.trigger('click')
    await flushPromises()

    expect(updateAccount).toHaveBeenCalledWith(1, { rate_multiplier: 0.25 })
    expect(showSuccess).toHaveBeenCalled()
    expect(getReport).toHaveBeenCalledTimes(2)
  })
})
