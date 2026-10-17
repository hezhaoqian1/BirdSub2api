import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UpstreamBalanceCell from '../UpstreamBalanceCell.vue'
import messages from '@/i18n/locales/zh/upstreamBalance'
import type { UpstreamBalanceState } from '@/api/admin/upstreamBalances'

const { saveConfig } = vi.hoisted(() => ({ saveConfig: vi.fn() }))
vi.mock('@/api/admin/upstreamBalances', () => ({ saveUpstreamBalanceConfig: saveConfig }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, values: Record<string, unknown> = {}) => {
    const message = messages[key.replace('upstreamBalance.', '') as keyof typeof messages] || key
    return message.replace(/\{(\w+)\}/g, (_, name) => String(values[name] ?? ''))
  } })
}))

function state(): UpstreamBalanceState {
  return { account_id: 7, owner_account_id: 7, mode: 'auto', token_configured: false, encryption_configured: true,
    snapshot: { fetched_at: 100, data: { scope: 'token', unlimited: true, unit: 'USD', source: 'new_api' } } }
}
function render(value = state()) {
  return mount(UpstreamBalanceCell, { props: { accountId: 7, name: 'Relay', state: value }, global: {
    stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot/><slot name="footer"/></div>' } }
  } })
}
beforeEach(() => vi.resetAllMocks())

describe('upstream balance presentation', () => {
  it('labels an unlimited token without implying an unlimited wallet', () => {
    const wrapper = render()
    expect(wrapper.text()).toContain('令牌额度 不限额')
    expect(wrapper.text()).toContain('账户余额未知')
    expect(wrapper.text()).not.toContain('账户余额 不限额')
  })
  it('retains a stale wallet value alongside the failure and timestamp', () => {
    const value = state()
    value.snapshot.data = { scope: 'account', remaining: 0, unit: 'USD', source: 'sub2api' }
    value.snapshot.error = '认证失败'
    const wrapper = render(value)
    expect(wrapper.text()).toContain('账户余额 0 USD')
    expect(wrapper.text()).toContain('上次余额 · 认证失败')
    expect(wrapper.text()).toContain('更新于')
  })
  it('keeps saved tokens write-only and shows actionable save errors', async () => {
    const value = state()
    value.mode = 'new_api'; value.token_configured = true; value.user_id = '12'
    const wrapper = render(value)
    await wrapper.get('[aria-label="配置余额查询"]').trigger('click')
    expect((wrapper.get('input[type="password"]').element as HTMLInputElement).value).toBe('')
    saveConfig.mockRejectedValue({ message: '请输入有效的 New API 用户 ID' })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('用户 ID')
    expect(saveConfig).toHaveBeenCalledWith(7, expect.objectContaining({ mode: 'new_api', access_token: '', user_id: '12' }))
  })
  it('emits the shared source without adding balances together', () => {
    const value = state(); value.owner_account_id = 3; value.mode = 'shared'
    const wrapper = render(value)
    expect(wrapper.text()).toContain('共享钱包 #3')
  })
})
