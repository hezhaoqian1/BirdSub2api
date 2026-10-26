import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountCapacityCell from '../AccountCapacityCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const account = (current: number) => ({
  id: 9,
  name: 'zzone-03',
  platform: 'anthropic',
  type: 'apikey',
  concurrency: 5,
  current_concurrency: current,
}) as unknown as Account

describe('AccountCapacityCell concurrency badge', () => {
  it('opens live requests when the account has slots in use', async () => {
    const w = mount(AccountCapacityCell, { props: { account: account(2) } })
    const button = w.get('button')
    expect(button.attributes('title')).toBe('admin.activeRequests.open')
    await button.trigger('click')
    expect(w.emitted('show-active-requests')?.[0]).toEqual([account(2)])
  })

  it('stays a plain badge when idle', () => {
    const w = mount(AccountCapacityCell, { props: { account: account(0) } })
    expect(w.find('button').exists()).toBe(false)
  })
})
