import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SecurityAlertSettings from '../SecurityAlertSettings.vue'
import zh from '@/i18n/locales/zh/securityAlerts'
import en from '@/i18n/locales/en/securityAlerts'

vi.mock('vue-i18n', () => ({ useI18n: () => ({
  t: (key: string) => {
    if (!key.startsWith('securityAlerts.')) return key
    const message = zh[key.replace('securityAlerts.', '') as keyof typeof zh]
    if (!message) throw new Error(`缺少翻译：${key}`)
    return message
  }
}) }))
const mocks = vi.hoisted(() => ({
  getConfig: vi.fn(), updateConfig: vi.fn(), sendTest: vi.fn(),
  getById: vi.fn(), list: vi.fn(),
  stepUp: vi.fn((fn: () => unknown) => fn()),
}))
vi.mock('@/api/admin/securityAlerts', () => ({ getConfig: mocks.getConfig, updateConfig: mocks.updateConfig, sendTest: mocks.sendTest }))
vi.mock('@/api/admin/users', () => ({ getById: mocks.getById, list: mocks.list }))
vi.mock('@/components/auth/TotpStepUpDialog.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/composables/useStepUp', () => ({ useStepUp: () => ({ run: mocks.stepUp }), isStepUpCancelled: () => false }))

function configView(watches = [{ user_id: 12, threshold: 50, note: '大客户' }]) {
  return {
    config: {
      enabled: true, trusted_cidrs: ['39.144.0.0/16'], daily_digest_hour: 9,
      channel_spend_floor: 100, channel_spend_multiplier: 3, channel_balance_check_minutes: 10,
      channel_low_balance_usd: 20, channel_low_balance_cny: 100, channel_runway_hours: 6,
      channel_quota_error_notify: true, user_balance_watches: watches,
    },
    wecom_webhook_configured: true,
    wecom_webhook_masked: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=5e74****0930',
    user_watch_status: watches.map(w => ({ user_id: w.user_id, email: 'big@example.com', username: '', balance: 8.2, found: true, below: true })),
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.useRealTimers()
  mocks.getConfig.mockResolvedValue(configView())
})

describe('企业微信告警设置', () => {
  it('中英文文案键保持一致', () => {
    expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort())
  })

  it('展示机器人掩码和关注用户当前余额', async () => {
    const wrapper = mount(SecurityAlertSettings)
    await flushPromises()
    expect(wrapper.text()).toContain('key=5e74****0930')
    expect(wrapper.text()).toContain('#12')
    expect(wrapper.text()).toContain('big@example.com')
    expect(wrapper.text()).toContain('低于提醒线')
    expect((wrapper.get('[data-test="channel_spend_floor"]').element as HTMLInputElement).value).toBe('100')
  })

  it('按 ID 搜索添加用户，保存时只提交本卡片字段并走二次验证', async () => {
    vi.useFakeTimers()
    mocks.getById.mockResolvedValue({ id: 34, email: 'new@example.com', username: 'new', balance: 2000 })
    mocks.updateConfig.mockImplementation(async (patch: { user_balance_watches: { user_id: number; threshold: number; note: string }[] }) => configView(patch.user_balance_watches))
    const wrapper = mount(SecurityAlertSettings)
    await flushPromises()

    await wrapper.get('[data-test="user-search"]').setValue('34')
    await vi.advanceTimersByTimeAsync(350)
    await flushPromises()
    expect(mocks.getById).toHaveBeenCalledWith(34)
    expect(mocks.list).not.toHaveBeenCalled()
    await wrapper.get('[data-test="add-user-34"]').trigger('click')
    expect(wrapper.text()).toContain('保存后生效')
    expect((wrapper.get('[data-test="threshold-34"]').element as HTMLInputElement).value).toBe('200')

    await wrapper.get('[data-test="channel_low_balance_usd"]').setValue('30')
    await wrapper.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(mocks.stepUp).toHaveBeenCalledOnce()
    const patch = mocks.updateConfig.mock.calls[0]![0]
    expect(patch.channel_low_balance_usd).toBe(30)
    expect(patch.user_balance_watches).toEqual([
      { user_id: 12, threshold: 50, note: '大客户' },
      { user_id: 34, threshold: 200, note: '' },
    ])
    expect(patch).not.toHaveProperty('trusted_cidrs')
    expect(patch).not.toHaveProperty('daily_digest_hour')
    expect(wrapper.text()).toContain('告警设置已保存')
  })

  it('提醒线必须大于 0', async () => {
    const wrapper = mount(SecurityAlertSettings)
    await flushPromises()
    await wrapper.get('[data-test="threshold-12"]').setValue('0')
    await wrapper.get('[data-test="save"]').trigger('click')
    await flushPromises()
    expect(mocks.updateConfig).not.toHaveBeenCalled()
    expect(wrapper.get('[role="alert"]').text()).toBe('提醒线必须大于 0')
  })
})
