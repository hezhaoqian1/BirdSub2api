import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserBalanceWatchModal from '../UserBalanceWatchModal.vue'
import type { AdminUser } from '@/types'
import type { UserBalanceWatch } from '@/api/admin/securityAlerts'

const mocks = vi.hoisted(() => ({
  setUserWatch: vi.fn(),
  removeUserWatch: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  // 带 te 的 t：extractI18nErrorMessage 据此判断错误码文案存在。
  t: Object.assign((key: string) => key, { te: () => true })
}))
vi.mock('@/api/admin/securityAlerts', () => ({
  setUserWatch: mocks.setUserWatch,
  removeUserWatch: mocks.removeUserWatch
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => mocks }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: mocks.t }) }))

enableAutoUnmount(afterEach)
beforeEach(() => vi.clearAllMocks())

const user = { id: 7, email: 'big@example.com', balance: 80 } as AdminUser

const mountModal = (props: { watch?: UserBalanceWatch | null; alertsEnabled?: boolean; webhookConfigured?: boolean } = {}) =>
  mount(UserBalanceWatchModal, {
    props: { show: false, user, watch: null, alertsEnabled: true, webhookConfigured: true, ...props },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } }
  })

// 弹窗打开时才按当前关注状态填表。
const open = async (wrapper: ReturnType<typeof mountModal>) => {
  await wrapper.setProps({ show: true })
}

describe('UserBalanceWatchModal', () => {
  it('saves a new watch with the rounded threshold and trimmed note', async () => {
    mocks.setUserWatch.mockResolvedValueOnce({ user_id: 7, threshold: 100.13, note: '大客户' })
    const wrapper = mountModal()
    await open(wrapper)
    expect(wrapper.find('[data-testid="balance-watch-remove"]').exists()).toBe(false)

    await wrapper.get('[data-testid="balance-watch-threshold"]').setValue('100.126')
    await wrapper.get('[data-testid="balance-watch-note"]').setValue('  大客户 ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(mocks.setUserWatch).toHaveBeenCalledWith(7, { threshold: 100.13, note: '大客户' })
    expect(mocks.showSuccess).toHaveBeenCalledWith('securityAlerts.userWatch.saved')
    expect(wrapper.emitted('saved')?.[0]).toEqual([{ user_id: 7, threshold: 100.13, note: '大客户' }])
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('prefills an existing watch and can remove it', async () => {
    mocks.removeUserWatch.mockResolvedValueOnce({ user_id: 7 })
    const wrapper = mountModal({ watch: { user_id: 7, threshold: 50, note: '月结' } })
    await open(wrapper)

    expect((wrapper.get('[data-testid="balance-watch-threshold"]').element as HTMLInputElement).value).toBe('50')
    expect((wrapper.get('[data-testid="balance-watch-note"]').element as HTMLInputElement).value).toBe('月结')
    await wrapper.get('[data-testid="balance-watch-remove"]').trigger('click')
    await flushPromises()

    expect(mocks.removeUserWatch).toHaveBeenCalledWith(7)
    expect(wrapper.emitted('removed')?.[0]).toEqual([7])
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it.each(['', '0', '-3', '0.001'])('rejects threshold %j without calling the API', async (value) => {
    const wrapper = mountModal()
    await open(wrapper)
    await wrapper.get('[data-testid="balance-watch-threshold"]').setValue(value)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(mocks.setUserWatch).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="balance-watch-error"]').text()).toBe('securityAlerts.thresholdInvalid')
  })

  it('maps backend error codes and keeps the dialog open', async () => {
    mocks.setUserWatch.mockRejectedValueOnce({ reason: 'USER_BALANCE_WATCH_LIMIT_REACHED', message: 'too many watched users' })
    const wrapper = mountModal()
    await open(wrapper)
    await wrapper.get('[data-testid="balance-watch-threshold"]').setValue('20')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(wrapper.get('[data-testid="balance-watch-error"]').text()).toBe('securityAlerts.userWatch.errors.USER_BALANCE_WATCH_LIMIT_REACHED')
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.get('[data-testid="balance-watch-save"]').attributes('disabled')).toBeUndefined()
  })

  it('warns when the balance is already below the line', async () => {
    const wrapper = mountModal()
    await open(wrapper)
    await wrapper.get('[data-testid="balance-watch-threshold"]').setValue('50')
    expect(wrapper.find('[data-testid="balance-watch-already-below"]').exists()).toBe(false)
    await wrapper.get('[data-testid="balance-watch-threshold"]').setValue('100')
    expect(wrapper.find('[data-testid="balance-watch-already-below"]').exists()).toBe(true)
  })

  it('warns when pushes cannot be delivered', async () => {
    const wrapper = mountModal({ alertsEnabled: false })
    await open(wrapper)
    expect(wrapper.get('[data-testid="balance-watch-channel-warning"]').text()).toBe('securityAlerts.userWatch.alertsDisabled')
    await wrapper.setProps({ alertsEnabled: true, webhookConfigured: false })
    expect(wrapper.get('[data-testid="balance-watch-channel-warning"]').text()).toBe('securityAlerts.userWatch.webhookMissing')
    await wrapper.setProps({ webhookConfigured: true })
    expect(wrapper.find('[data-testid="balance-watch-channel-warning"]').exists()).toBe(false)
  })
})
