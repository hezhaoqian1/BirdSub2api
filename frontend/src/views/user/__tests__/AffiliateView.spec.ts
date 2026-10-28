import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AffiliateView from '../AffiliateView.vue'

const { copyToClipboard, getAffiliateDetail, updateAffiliateCode, grantAffiliateAgentCredit } = vi.hoisted(() => ({
  copyToClipboard: vi.fn(),
  getAffiliateDetail: vi.fn(),
  updateAffiliateCode: vi.fn(),
  grantAffiliateAgentCredit: vi.fn(),
}))

vi.mock('@/api/user', () => ({
  default: {
    getAffiliateDetail,
    updateAffiliateCode,
    transferAffiliateQuota: vi.fn(),
    grantAffiliateAgentCredit,
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

  describe('agent test credit', () => {
    const invitee = {
      user_id: 42,
      email: 'a***@example.com',
      username: 'alice',
      created_at: '2026-10-27T08:00:00Z',
      total_rebate: 0,
      agent_granted: 5,
    }
    const agentDetail = {
      ...baseDetail,
      aff_count: 1,
      invitees: [invitee],
      agent: { credit_limit: 100, credit_used: 35, credit_remaining: 65 },
    }

    const openGrant = async () => {
      getAffiliateDetail.mockResolvedValue({ ...agentDetail })
      const wrapper = mountView()
      await flushPromises()
      await wrapper.get('[data-testid="affiliate-agent-grant-42"]').trigger('click')
      return wrapper
    }

    it('shows nothing agent-related to regular users', async () => {
      getAffiliateDetail.mockResolvedValue({ ...baseDetail, invitees: [{ ...invitee, agent_granted: 0 }] })
      const wrapper = mountView()
      await flushPromises()

      expect(wrapper.find('[data-testid="affiliate-agent-card"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="affiliate-agent-grant-42"]').exists()).toBe(false)
      expect(wrapper.text()).not.toContain('affiliate.invitees.columns.agentGranted')
    })

    it('shows the allowance and grants credit with required notes', async () => {
      grantAffiliateAgentCredit.mockResolvedValue({
        grant_id: 7, invitee_user_id: 42, amount: 10, credit_limit: 100, credit_used: 45, credit_remaining: 55,
      })
      const wrapper = await openGrant()
      expect(wrapper.get('[data-testid="affiliate-agent-card"]').text()).toContain('affiliate.agent.creditRemaining')
      expect(wrapper.text()).toContain('affiliate.invitees.columns.agentGranted')

      await wrapper.get('[data-testid="affiliate-agent-grant-amount"]').setValue('10')
      await wrapper.get('[data-testid="affiliate-agent-grant-notes"]').setValue('  new customer trial  ')
      await wrapper.get('[data-testid="affiliate-agent-grant-submit"]').trigger('click')
      await flushPromises()

      expect(grantAffiliateAgentCredit).toHaveBeenCalledTimes(1)
      const [payload, key] = grantAffiliateAgentCredit.mock.calls[0]
      expect(payload).toEqual({ invitee_user_id: 42, amount: 10, notes: 'new customer trial' })
      expect(key).toMatch(/^affiliate-agent-grant-/)
      expect(wrapper.find('[data-testid="dialog"]').exists()).toBe(false)
      // 发放后重新拉取详情，额度和已发金额随之更新。
      expect(getAffiliateDetail).toHaveBeenCalledTimes(2)
    })

    it('checks amount and notes locally before calling the API', async () => {
      const wrapper = await openGrant()

      await wrapper.get('[data-testid="affiliate-agent-grant-amount"]').setValue('0')
      await wrapper.get('[data-testid="affiliate-agent-grant-notes"]').setValue('trial')
      await wrapper.get('[data-testid="affiliate-agent-grant-submit"]').trigger('click')
      expect(wrapper.get('[data-testid="affiliate-agent-grant-error"]').text()).toBe('affiliate.agent.errors.AMOUNT_INVALID')

      await wrapper.get('[data-testid="affiliate-agent-grant-amount"]').setValue('65.5')
      await wrapper.get('[data-testid="affiliate-agent-grant-submit"]').trigger('click')
      expect(wrapper.get('[data-testid="affiliate-agent-grant-error"]').text()).toBe('affiliate.agent.errors.AMOUNT_EXCEEDS_REMAINING')

      await wrapper.get('[data-testid="affiliate-agent-grant-amount"]').setValue('5')
      await wrapper.get('[data-testid="affiliate-agent-grant-notes"]').setValue('   ')
      await wrapper.get('[data-testid="affiliate-agent-grant-submit"]').trigger('click')
      expect(wrapper.get('[data-testid="affiliate-agent-grant-error"]').text()).toBe('affiliate.agent.errors.AFFILIATE_AGENT_NOTES_REQUIRED')

      expect(grantAffiliateAgentCredit).not.toHaveBeenCalled()
    })

    it('maps backend reasons and retries with the same idempotency key', async () => {
      grantAffiliateAgentCredit.mockRejectedValueOnce({ status: 400, reason: 'AFFILIATE_AGENT_CREDIT_INSUFFICIENT', message: 'no credit' })
      grantAffiliateAgentCredit.mockResolvedValueOnce({
        grant_id: 8, invitee_user_id: 42, amount: 5, credit_limit: 100, credit_used: 40, credit_remaining: 60,
      })
      const wrapper = await openGrant()

      await wrapper.get('[data-testid="affiliate-agent-grant-amount"]').setValue('5')
      await wrapper.get('[data-testid="affiliate-agent-grant-notes"]').setValue('trial')
      await wrapper.get('[data-testid="affiliate-agent-grant-submit"]').trigger('click')
      await flushPromises()
      expect(wrapper.get('[data-testid="affiliate-agent-grant-error"]').text()).toBe('affiliate.agent.errors.AFFILIATE_AGENT_CREDIT_INSUFFICIENT')
      expect(wrapper.find('[data-testid="dialog"]').exists()).toBe(true)

      await wrapper.get('[data-testid="affiliate-agent-grant-submit"]').trigger('click')
      await flushPromises()
      expect(grantAffiliateAgentCredit).toHaveBeenCalledTimes(2)
      expect(grantAffiliateAgentCredit.mock.calls[1][1]).toBe(grantAffiliateAgentCredit.mock.calls[0][1])
    })

    it('disables granting once the allowance is used up', async () => {
      getAffiliateDetail.mockResolvedValue({
        ...agentDetail,
        agent: { credit_limit: 35, credit_used: 35, credit_remaining: 0 },
      })
      const wrapper = mountView()
      await flushPromises()
      expect(wrapper.get('[data-testid="affiliate-agent-grant-42"]').attributes('disabled')).toBeDefined()
    })
  })
})
