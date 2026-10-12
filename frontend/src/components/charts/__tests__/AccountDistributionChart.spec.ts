import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountDistributionChart from '../AccountDistributionChart.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('vue-chartjs', () => ({ Doughnut: { props: ['data', 'options'], template: '<div />' } }))

const accounts = [
  { account_id: 1, account_name: 'Shared name', requests: 4, total_tokens: 900, cost: 15, actual_cost: 12, account_cost: 2 },
  { account_id: 2, account_name: 'Shared name', requests: 2, total_tokens: 100, cost: 10, actual_cost: 8, account_cost: 5 },
]

describe('AccountDistributionChart', () => {
  it('ranks by upstream cost independently of user charges and shows distinct account IDs', async () => {
    const wrapper = mount(AccountDistributionChart, { props: { accountStats: accounts } })
    let rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('Shared name (#2)')
    expect(rows[0].findAll('td')[3].text()).toBe('$8.00')
    expect(rows[0].findAll('td')[4].text()).toBe('$5.00')
    await wrapper.setProps({ metric: 'tokens' })
    rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('Shared name (#1)')
    expect(accounts[0].account_id).toBe(1)
  })

  it('shows a retryable error instead of empty statistics', async () => {
    const wrapper = mount(AccountDistributionChart, { props: { accountStats: [], error: true } })
    expect(wrapper.get('[role="alert"]').text()).toContain('accountStatsFailed')
    await wrapper.get('[role="alert"] button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
    await wrapper.setProps({ error: false })
    expect(wrapper.text()).toContain('noDataAvailable')
  })
})
