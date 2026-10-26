import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ActiveRequestsDialog from '../ActiveRequestsDialog.vue'
import type { ActiveRequestsResponse } from '@/types'

const mocks = vi.hoisted(() => ({
  users: { getActiveRequests: vi.fn() },
  accounts: { getActiveRequests: vi.fn() },
  showError: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { users: mocks.users, accounts: mocks.accounts } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }) }))

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
})

const stubs = {
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
  AutoRefreshButton: true,
  GroupBadge: { props: ['name'], template: '<span class="group-badge">{{ name }}</span>' },
  PlatformIcon: true,
  Icon: true,
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => { resolve = res })
  return { promise, resolve }
}

const accountResponse: ActiveRequestsResponse = {
  current: 3,
  max: 10,
  waiting: 1,
  items: [
    { slot_id: 's1', live: false, recorded: true, request_id: 'r1', elapsed_seconds: 133, model: 'claude-opus-5', user_id: 1, user_email: 'alice@example.com', api_key_id: 7, api_key_name: 'cc-main', group_id: 3, group_name: 'Claude-Pro', account_id: 9 },
    { slot_id: 's2', live: false, recorded: true, request_id: 'r2', elapsed_seconds: 41, model: 'claude-opus-5', user_id: 1, user_email: 'alice@example.com', api_key_id: 7, group_id: 3, group_name: 'Claude-Pro', account_id: 9 },
    { slot_id: 's3', live: false, recorded: false, elapsed_seconds: 8, account_id: 9 },
  ],
}

function mountDialog(mode: 'user' | 'account', show = true) {
  return mount(ActiveRequestsDialog, {
    props: { show, mode, target: { id: 9, title: 'zzone-03', platform: 'anthropic' } },
    global: { stubs },
  })
}

describe('ActiveRequestsDialog', () => {
  it('shows who is holding an account and summarises by user', async () => {
    mocks.accounts.getActiveRequests.mockResolvedValue(accountResponse)
    const w = mountDialog('account')
    await flushPromises()

    expect(mocks.accounts.getActiveRequests).toHaveBeenCalledWith(9)
    expect(mocks.users.getActiveRequests).not.toHaveBeenCalled()
    const text = w.text()
    expect(text).toContain('alice@example.com')
    expect(text).toContain('cc-main #7')
    expect(text).toContain('claude-opus-5')
    expect(text).toContain('2m13s')
    expect(text).toContain('×2')
    // 未记录详情的行与提示
    expect(text).toContain('admin.activeRequests.unrecorded')
    expect(text).toContain('admin.activeRequests.unrecordedHint')
    expect(w.findAll('tbody tr')).toHaveLength(3)
  })

  it('summarises a user by group and marks requests without an account', async () => {
    mocks.users.getActiveRequests.mockResolvedValue({
      current: 2,
      max: 0,
      waiting: 0,
      items: [
        { slot_id: 'u1', live: false, recorded: true, elapsed_seconds: 5, model: 'gemini-3-pro', user_id: 1, group_id: 4, group_name: 'Gemini', account_id: 12, account_name: 'yhshu-01' },
        { slot_id: 'u2', live: false, recorded: true, elapsed_seconds: 1, model: 'gemini-3-pro', user_id: 1, group_id: 4, group_name: 'Gemini' },
      ],
    } satisfies ActiveRequestsResponse)
    const w = mountDialog('user')
    await flushPromises()

    expect(mocks.users.getActiveRequests).toHaveBeenCalledWith(9)
    const text = w.text()
    expect(text).toContain('admin.activeRequests.byGroup')
    expect(text).toContain('Gemini')
    expect(text).toContain('×2')
    expect(text).toContain('yhshu-01')
    expect(text).toContain('admin.activeRequests.noAccount')
    expect(text).toContain('admin.activeRequests.unlimited')
    expect(text).not.toContain('admin.activeRequests.unrecordedHint')
  })

  it('shows an empty state when nothing is running', async () => {
    mocks.accounts.getActiveRequests.mockResolvedValue({ current: 0, max: 5, waiting: 0, items: [] })
    const w = mountDialog('account')
    await flushPromises()
    expect(w.text()).toContain('admin.activeRequests.empty')
  })

  it('does not fetch while closed', async () => {
    mountDialog('account', false)
    await flushPromises()
    expect(mocks.accounts.getActiveRequests).not.toHaveBeenCalled()
  })

  it('keeps the new target when the previous target responds late', async () => {
    const slow = deferred<ActiveRequestsResponse>()
    mocks.accounts.getActiveRequests
      .mockReturnValueOnce(slow.promise)
      .mockResolvedValueOnce({ current: 0, max: 5, waiting: 0, items: [] })
    const w = mountDialog('account')
    await w.setProps({ target: { id: 10, title: 'other', platform: 'anthropic' } })
    await flushPromises()
    expect(w.text()).toContain('admin.activeRequests.empty')

    slow.resolve(accountResponse)
    await flushPromises()
    expect(w.text()).not.toContain('alice@example.com')
    expect(w.text()).toContain('admin.activeRequests.empty')
  })

  it('shows a load error instead of the empty state', async () => {
    mocks.accounts.getActiveRequests.mockRejectedValue(new Error('boom'))
    const w = mountDialog('account')
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledWith('admin.activeRequests.loadFailed')
    expect(w.text()).toContain('admin.activeRequests.loadFailed')
    expect(w.text()).not.toContain('admin.activeRequests.empty')
  })

  it('does not show a running time for live sessions', async () => {
    mocks.accounts.getActiveRequests.mockResolvedValue({
      current: 1,
      max: 5,
      waiting: 0,
      items: [{ slot_id: 'l1', live: true, recorded: false, elapsed_seconds: 0, account_id: 9 }],
    } satisfies ActiveRequestsResponse)
    const w = mountDialog('account')
    await flushPromises()
    const cells = w.findAll('tbody td')
    expect(cells[0].text()).toBe('admin.activeRequests.liveSession')
    expect(cells[cells.length - 1].text()).toBe('-')
    expect(w.text()).not.toContain('admin.activeRequests.unrecordedHint')
  })
})
