import { describe, expect, it, vi, beforeEach } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'

const isV1 = vi.fn(() => false)
const hybrid = vi.hoisted(() => vi.fn())
vi.mock('@/api/channelMonitorHybrid', () => ({ getHybridSnapshot: hybrid }))
vi.mock('../ChannelStatusHybridView.vue', () => ({ default: defineComponent({ setup: () => () => h('div', { 'data-testid': 'hybrid' }) }) }))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: defineComponent({ setup: (_, { slots }) => () => h('div', { 'data-testid': 'layout' }, slots.default?.()) }),
}))

vi.mock('@/components/common/LoadingSpinner.vue', () => ({
  default: defineComponent({ setup: () => () => h('span') }),
}))

vi.mock('@/utils/featureFlags', () => ({
  isChannelMonitorV1Mode: () => isV1(),
}))

vi.mock('../ChannelStatusV1View.vue', () => ({
  default: defineComponent({ name: 'ChannelStatusV1View', setup: () => () => h('div', { 'data-testid': 'v1' }) }),
}))
vi.mock('../ChannelStatusV2View.vue', () => ({
  default: defineComponent({ name: 'ChannelStatusV2View', setup: () => () => h('div', { 'data-testid': 'v2' }) }),
}))

import ChannelStatusView from '../ChannelStatusView.vue'

describe('ChannelStatusView mode switch', () => {
  beforeEach(() => {
    isV1.mockReset()
    hybrid.mockResolvedValue({ config: { enabled: false }, items: [] })
  })

  it('renders V2 when not in v1 mode', async () => {
    isV1.mockReturnValue(false)
    const wrapper = mount(ChannelStatusView)
    await flushPromises()
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="v1"]').exists()).toBe(false)
  })

  it('renders V1 when in v1 mode', async () => {
    isV1.mockReturnValue(true)
    const wrapper = mount(ChannelStatusView)
    await flushPromises()
    expect(wrapper.find('[data-testid="v1"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(false)
  })
  it('prefers enabled hybrid mode over legacy selection', async () => {
    hybrid.mockResolvedValue({ config: { enabled: true }, items: [] })
    isV1.mockReturnValue(true)
    const wrapper = mount(ChannelStatusView)
    await flushPromises()
    expect(wrapper.find('[data-testid="hybrid"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="v1"]').exists()).toBe(false)
  })

  it('keeps the layout mounted while the hybrid snapshot is pending', async () => {
    let resolve!: (value: unknown) => void
    hybrid.mockReturnValue(new Promise((r) => { resolve = r }))
    const wrapper = mount(ChannelStatusView)
    expect(wrapper.find('[data-testid="layout"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="channel-status-pending"]').exists()).toBe(true)
    resolve({ config: { enabled: false }, items: [] })
    await flushPromises()
    expect(wrapper.find('[data-testid="channel-status-pending"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="v2"]').exists()).toBe(true)
  })
})
