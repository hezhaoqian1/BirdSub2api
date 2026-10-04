import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsErrorDetailModal from '../OpsErrorDetailModal.vue'

const mocks = vi.hoisted(() => ({
  getRequestErrorDetail: vi.fn(),
  listRequestErrorUpstreamErrors: vi.fn()
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getRequestErrorDetail: mocks.getRequestErrorDetail,
    getUpstreamErrorDetail: vi.fn(),
    listRequestErrorUpstreamErrors: mocks.listRequestErrorUpstreamErrors
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('OpsErrorDetailModal', () => {
  beforeEach(() => {
    mocks.getRequestErrorDetail.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
  })

  it('prioritizes upstream root cause and deduplicates diagnostic payloads', async () => {
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 1,
      created_at: '2026-08-19T00:00:00Z',
      phase: 'request',
      type: 'upstream_error',
      error_owner: 'provider',
      error_source: 'gateway',
      severity: 'P1',
      status_code: 502,
      upstream_status_code: 429,
      platform: 'openai',
      model: 'gpt-5.6',
      resolved: false,
      request_id: 'rid-1',
      message: 'All available accounts exhausted',
      error_body: '{"error":"same"}',
      upstream_error_message: 'provider rate limit exhausted',
      upstream_error_detail: '{"error":"same"}',
      upstream_errors: '[]',
      account_name: 'account',
      group_name: 'group',
      is_business_limited: false
    })

    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /></div>' },
          Icon: true
        }
      }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('provider rate limit exhausted')
    expect(wrapper.text()).toContain('admin.ops.errorDetail.upstreamStatus')
    expect(wrapper.text()).toContain('429')
    expect(wrapper.findAll('pre')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('admin.ops.errorDetail.payloads.upstream_detail')
    expect(wrapper.find('[data-testid="error-detail-request-snapshot"]').exists()).toBe(false)
  })

  it('renders the recorded request snapshot with summary and copy action', async () => {
    const body = '{"model":"claude-sonnet-4-6","max_tokens":20000,"messages":[{"role":"user","content":"hi"}]}'
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 2,
      created_at: '2026-10-05T00:00:00Z',
      phase: 'upstream',
      type: 'upstream_error',
      severity: 'P1',
      status_code: 524,
      platform: 'anthropic',
      model: 'claude-sonnet-4-6',
      resolved: false,
      message: 'Upstream request failed',
      error_body: '',
      is_business_limited: false,
      request_snapshot: JSON.stringify({
        summary: { size_bytes: body.length, format: 'json', model: 'claude-sonnet-4-6', max_tokens: 20000, message_count: 1 },
        truncated: false,
        body
      })
    })

    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 2, errorType: 'request' },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true } }
    })
    await flushPromises()

    const section = wrapper.find('[data-testid="error-detail-request-snapshot"]')
    expect(section.exists()).toBe(true)
    expect(section.text()).toContain('admin.ops.errorDetail.requestSnapshot.fields.max_tokens')
    expect(section.text()).toContain('20000')
    expect(section.text()).toContain('"content": "hi"')
    expect(wrapper.find('[data-testid="error-detail-request-snapshot-copy"]').exists()).toBe(true)
  })

  it('renders head and tail for a truncated request snapshot', async () => {
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 3,
      created_at: '2026-10-05T00:00:00Z',
      phase: 'upstream',
      type: 'upstream_error',
      severity: 'P1',
      status_code: 524,
      resolved: false,
      message: 'x',
      error_body: '',
      is_business_limited: false,
      request_snapshot: JSON.stringify({
        summary: { size_bytes: 900000, format: 'json' },
        truncated: true,
        head: '{"model":"m","messages":[HEAD-PART',
        tail: 'TAIL-PART]}',
        omitted_bytes: 834464
      })
    })

    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 3, errorType: 'request' },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true } }
    })
    await flushPromises()

    const section = wrapper.find('[data-testid="error-detail-request-snapshot"]')
    expect(section.text()).toContain('admin.ops.errorDetail.requestSnapshot.truncatedHint')
    expect(section.text()).toContain('HEAD-PART')
    expect(section.text()).toContain('TAIL-PART')
  })
})
