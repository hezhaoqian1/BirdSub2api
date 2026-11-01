import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ChannelFidelityPanel from '../ChannelFidelityPanel.vue'
import type { ChannelFidelityCheck, ChannelFidelityStatusView } from '@/api/admin/channelFidelity'

const mocks = vi.hoisted(() => ({
  getStatus: vi.fn(), updateConfig: vi.fn(), runAll: vi.fn(), runOne: vi.fn(), resetBaseline: vi.fn(),
}))
vi.mock('@/api/admin/channelFidelity', () => mocks)
vi.mock('@/components/common/BaseDialog.vue', () => ({
  default: { props: ['show', 'title'], template: '<div v-if="show" data-test="dialog"><slot /></div>' },
}))

function check(overrides: Partial<ChannelFidelityCheck> = {}): ChannelFidelityCheck {
  return {
    id: 1, account_id: 161, model: 'deepseek-v4.1-flash', upstream_model: 'deepseek-v4.1-flash',
    checked_at: '2026-11-01T02:00:00Z', status: 'pass', is_recheck: false,
    system_check: 'pass', thinking_check: 'pass', json_check: 'pass', answer_check: 'pass', prompt_check: 'pass',
    prompt_tokens: 81, baseline_prompt_tokens: 81, completion_tokens: 20, reasoning_tokens: 0, reasoning_chars: 0,
    latency_ms: 4200, http_status: 200, message: '', response_excerpt: '{"code": "482913", "answer": 579}',
    probe_kind: 'deepseek', signature_check: 'skip', media_check: 'skip', canary_check: 'skip', meta_check: 'skip', signature_prefix: '',
    ...overrides,
  }
}

function statusView(): ChannelFidelityStatusView {
  return {
    available: true,
    config: {
      enabled: true, interval_minutes: 15, recheck_seconds: 60, models: ['deepseek-v4.1-flash', 'deepseek-v4-pro'], prompt_token_tolerance: 10,
      gemini_models: ['gemini-3.8-flash', 'gemini-4-argon'], gemini_account_ids: [], gemini_signature_prefix: 'AY',
    },
    notify_enabled: true,
    running: false,
    last_sweep_at: '2026-11-01T02:00:00Z',
    rows: [
      {
        kind: 'deepseek', account_id: 135, account_name: 'Aizzz DeepSeek', schedulable: true, model: 'deepseek-v4.1-flash',
        upstream_model: 'deepseek-v4.1-flash', baseline_prompt_tokens: 59, alerting: true,
        latest: check({ account_id: 135, status: 'fail', thinking_check: 'fail', reasoning_chars: 812, prompt_tokens: 59, baseline_prompt_tokens: 59, message: '关闭思考无效（仍返回 812 字思考）' }),
        history: [
          { checked_at: '2026-11-01T01:45:00Z', status: 'pass', is_recheck: false },
          { checked_at: '2026-11-01T02:00:00Z', status: 'fail', is_recheck: false },
          { checked_at: '2026-11-01T02:01:00Z', status: 'fail', is_recheck: true },
        ],
      },
      {
        kind: 'deepseek', account_id: 157, account_name: 'lcodex', schedulable: true, model: 'deepseek-v4.1-flash',
        upstream_model: 'deepseek-flash', baseline_prompt_tokens: 81, alerting: false,
        latest: check({ account_id: 157, upstream_model: 'deepseek-flash' }), history: [],
      },
      {
        kind: 'deepseek', account_id: 123, account_name: '鲜藕鸡', schedulable: false, model: 'deepseek-v4-pro',
        upstream_model: 'deepseek-v4-pro', baseline_prompt_tokens: null, alerting: false,
        latest: check({ account_id: 123, status: 'error', http_status: 502, message: '上游返回 HTTP 502' }), history: [],
      },
      {
        kind: 'gemini_vt', account_id: 174, account_name: 'zzshu Gemini VT', schedulable: true, model: 'gemini-3.8-flash',
        upstream_model: 'gemini-3.8-flash', baseline_prompt_tokens: 7, alerting: true,
        latest: check({
          account_id: 174, model: 'gemini-3.8-flash', upstream_model: 'gemini-3.8-flash', status: 'fail', probe_kind: 'gemini_vt',
          system_check: 'skip', thinking_check: 'skip', json_check: 'skip', answer_check: 'skip',
          signature_check: 'pass', media_check: 'pass', canary_check: 'fail', meta_check: 'fail', signature_prefix: 'AY89a1',
          prompt_tokens: 8, message: '上一轮的 Vertex 签名被拒（HTTP 400：Invalid thought signature.）',
          response_excerpt: '[视频] Tokyo; the woman is Saeka Shimada.',
        }),
        history: [],
      },
    ],
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  mocks.getStatus.mockResolvedValue(statusView())
})

describe('保真体检面板', () => {
  it('按账号展示各项检查、上游模型、停用与告警标记', async () => {
    const wrapper = mount(ChannelFidelityPanel)
    await flushPromises()
    const aizzz = wrapper.get('[data-test="fidelity-row-135-deepseek-v4.1-flash"]')
    expect(aizzz.text()).toContain('告警中')
    expect(aizzz.text()).toContain('✗')
    expect(aizzz.text()).toContain('关闭思考无效')
    expect(aizzz.findAll('span.rounded-full')).toHaveLength(3)

    const lcodex = wrapper.get('[data-test="fidelity-row-157-deepseek-flash"]')
    expect(lcodex.text()).toContain('上游 deepseek-flash')
    expect(lcodex.text()).toContain('81 / 81')

    const xoj = wrapper.get('[data-test="fidelity-row-123-deepseek-v4-pro"]')
    expect(xoj.text()).toContain('已停用 · 不告警')
    expect(xoj.text()).toContain('请求出错')
    expect(xoj.text()).toContain('待记录')

    expect(wrapper.text()).toContain('已开启')
  })

  it('保存设置时把逗号分隔的模型拆成数组', async () => {
    mocks.updateConfig.mockImplementation(async (cfg: Record<string, unknown>) => cfg)
    const wrapper = mount(ChannelFidelityPanel)
    await flushPromises()
    await wrapper.get('[data-test="fidelity-interval"]').setValue('30')
    await wrapper.get('[data-test="fidelity-models"]').setValue('deepseek-v4.1-flash，deepseek-v4-pro, deepseek-flash')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      enabled: true,
      interval_minutes: 30,
      models: ['deepseek-v4.1-flash', 'deepseek-v4-pro', 'deepseek-flash'],
      gemini_models: ['gemini-3.8-flash', 'gemini-4-argon'],
      gemini_account_ids: [],
    }))
    expect(wrapper.text()).toContain('体检设置已保存')
  })

  it('Gemini VT 单独成表，展示签名 / 视频 / 旧签名 / 元数据四项', async () => {
    const wrapper = mount(ChannelFidelityPanel)
    await flushPromises()
    const section = wrapper.get('[data-test="fidelity-section-gemini_vt"]')
    expect(section.text()).toContain('Gemini VT')
    expect(section.text()).toContain('gs:// 视频')
    expect(section.text()).toContain('旧签名')
    expect(section.text()).not.toContain('关思考')
    const row = section.get('[data-test="fidelity-row-174-gemini-3.8-flash"]')
    expect(row.text()).toContain('告警中')
    expect(row.text()).toContain('Invalid thought signature')
    expect(row.findAll('td.text-red-500').length).toBeGreaterThanOrEqual(2)
    // DeepSeek 表里没有 Gemini 行。
    expect(wrapper.get('[data-test="fidelity-section-deepseek"]').text()).not.toContain('zzshu')
  })

  it('保存 Gemini 额外账号 ID，非法输入不提交', async () => {
    mocks.updateConfig.mockImplementation(async (cfg: Record<string, unknown>) => cfg)
    const wrapper = mount(ChannelFidelityPanel)
    await flushPromises()
    await wrapper.get('[data-test="fidelity-gemini-accounts"]').setValue('174, abc')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.updateConfig).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('账号 ID 只能填正整数')

    await wrapper.get('[data-test="fidelity-gemini-accounts"]').setValue('174，180')
    await wrapper.get('[data-test="fidelity-gemini-models"]').setValue('gemini-3.8-flash')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.updateConfig).toHaveBeenCalledWith(expect.objectContaining({
      gemini_models: ['gemini-3.8-flash'],
      gemini_account_ids: [174, 180],
      gemini_signature_prefix: 'AY',
    }))
  })

  it('单个体检用请求模型调用接口并展示结论', async () => {
    mocks.runOne.mockResolvedValue(check({ account_id: 157, upstream_model: 'deepseek-flash' }))
    const wrapper = mount(ChannelFidelityPanel)
    await flushPromises()
    await wrapper.get('[data-test="fidelity-check-157-deepseek-flash"]').trigger('click')
    await flushPromises()
    expect(mocks.runOne).toHaveBeenCalledWith(157, 'deepseek-v4.1-flash')
    expect(wrapper.text()).toContain('lcodex · deepseek-v4.1-flash：全部通过')
  })

  it('立即全部体检', async () => {
    mocks.runAll.mockResolvedValue({ started: true })
    const wrapper = mount(ChannelFidelityPanel)
    await flushPromises()
    await wrapper.get('[data-test="fidelity-run-all"]').trigger('click')
    await flushPromises()
    expect(mocks.runAll).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('已开始体检')
    expect(wrapper.get('[data-test="fidelity-run-all"]').text()).toBe('体检中…')
    wrapper.unmount()
  })
})
