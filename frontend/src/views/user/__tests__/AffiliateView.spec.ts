import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AffiliateView from '../AffiliateView.vue'

const { copyToClipboard, getAffiliateDetail, updateAffiliateCode } = vi.hoisted(() => ({
  copyToClipboard: vi.fn(),
  getAffiliateDetail: vi.fn(),
  updateAffiliateCode: vi.fn(),
}))

vi.mock('@/api/user', () => ({
  default: {
    getAffiliateDetail,
    updateAffiliateCode,
    transferAffiliateQuota: vi.fn(),
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    refreshUser: vi.fn(),
  }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const mountView = () =>
  mount(AffiliateView, {
    global: {
      stubs: {
        AppLayout: { template: '<main><slot /></main>' },
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show" data-testid="dialog"><slot /><slot name="footer" /></div>',
        },
        Icon: true,
      },
    },
  })

describe('AffiliateView', () => {
  const affiliateCode = 'affiliate-code-that-is-long-enough-to-overflow-a-mobile-viewport'
  const baseDetail = {
    user_id: 1,
    aff_code: affiliateCode,
    inviter_id: null,
    aff_count: 0,
    aff_quota: 0,
    aff_frozen_quota: 0,
    aff_history_quota: 0,
    effective_rebate_rate_percent: 10,
    invitees: [],
  }

  beforeEach(() => {
    vi.clearAllMocks()
    copyToClipboard.mockResolvedValue(true)
    getAffiliateDetail.mockResolvedValue({ ...baseDetail })
  })

  it('stacks long values and copy controls on mobile while retaining desktop rows', async () => {
    const wrapper = mountView()

    await flushPromises()

    const values = wrapper.findAll('code')
    expect(values).toHaveLength(2)
    for (const value of values) {
      expect(value.classes()).toEqual(expect.arrayContaining([
        'min-w-0',
        'break-all',
        'sm:flex-1',
        'sm:truncate',
      ]))
      expect(Array.from(value.element.parentElement?.classList ?? [])).toEqual(expect.arrayContaining([
        'flex-col',
        'items-stretch',
        'sm:flex-row',
        'sm:items-center',
      ]))
    }

    const copyButtons = wrapper.findAll('button').filter((button) =>
      ['affiliate.copyCode', 'affiliate.copyLink'].includes(button.text()),
    )
    expect(copyButtons).toHaveLength(2)
    for (const button of copyButtons) {
      expect(button.classes()).toEqual(expect.arrayContaining([
        'w-full',
        'sm:w-auto',
        'sm:shrink-0',
      ]))
    }

    await copyButtons[0].trigger('click')
    await copyButtons[1].trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenNthCalledWith(1, affiliateCode, 'affiliate.codeCopied')
    expect(copyToClipboard).toHaveBeenNthCalledWith(
      2,
      `${window.location.origin}/r/${encodeURIComponent(affiliateCode)}`,
      'affiliate.linkCopied',
    )
  })

  it('lets the user pick a custom code (upper-cased) and shows the new one', async () => {
    updateAffiliateCode.mockResolvedValue({ ...baseDetail, aff_code: 'VIP2026' })
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="affiliate-edit-code"]').trigger('click')
    await wrapper.get('[data-testid="affiliate-custom-code-input"]').setValue(' vip2026 ')
    await wrapper.get('[data-testid="affiliate-custom-code-save"]').trigger('click')
    await flushPromises()

    expect(updateAffiliateCode).toHaveBeenCalledWith('VIP2026')
    expect(wrapper.find('[data-testid="dialog"]').exists()).toBe(false)
    expect(wrapper.findAll('code')[0].text()).toBe('VIP2026')
  })

  it('validates the format locally and maps backend reasons to messages', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="affiliate-edit-code"]').trigger('click')
    await wrapper.get('[data-testid="affiliate-custom-code-input"]').setValue('ab-c')
    await wrapper.get('[data-testid="affiliate-custom-code-save"]').trigger('click')
    await flushPromises()
    expect(updateAffiliateCode).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('affiliate.customCode.errors.AFFILIATE_CODE_INVALID')

    updateAffiliateCode.mockRejectedValue({ status: 409, reason: 'AFFILIATE_CODE_TAKEN', message: 'taken' })
    await wrapper.get('[data-testid="affiliate-custom-code-input"]').setValue('HOTCODE')
    await wrapper.get('[data-testid="affiliate-custom-code-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('affiliate.customCode.errors.AFFILIATE_CODE_TAKEN')
    expect(wrapper.find('[data-testid="dialog"]').exists()).toBe(true)
  })

  it('hides editing for admin-set codes and disables it during the cooldown', async () => {
    getAffiliateDetail.mockResolvedValueOnce({ ...baseDetail, aff_code_custom: true })
    const managed = mountView()
    await flushPromises()
    expect(managed.find('[data-testid="affiliate-edit-code"]').exists()).toBe(false)
    expect(managed.text()).toContain('affiliate.customCode.managedByAdmin')

    const nextWeek = new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString()
    getAffiliateDetail.mockResolvedValueOnce({ ...baseDetail, aff_code_change_available_at: nextWeek })
    const cooling = mountView()
    await flushPromises()
    expect(cooling.get('[data-testid="affiliate-edit-code"]').attributes('disabled')).toBeDefined()
    expect(cooling.text()).toContain('affiliate.customCode.nextChangeAt')
  })
})
