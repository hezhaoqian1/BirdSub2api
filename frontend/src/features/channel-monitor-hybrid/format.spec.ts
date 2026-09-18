import { describe, expect, it } from 'vitest'
import type { HybridItem, HybridMinute } from '@/api/channelMonitorHybrid'
import { currentHybridStatus, hybridSuccess, hybridTimeline } from './format'

describe('hybrid monitor presentation', () => {
  const now = Date.parse('2026-09-18T10:02:30Z')
  const point = { minute: '2026-09-18T10:01:00Z', observed_at: '2026-09-18T10:02:10Z', source: 'passive', status: 'failed', reasons: ['ttft'], success_rate: 1, ttft_ms: 28000, finalized: true } as HybridMinute
  const item = { rule: { enabled: true }, minutes: [point] } as HybridItem
  it('keeps actual success separate from performance failure', () => {
    expect(hybridSuccess(point)).toBe('100.0%')
    expect(currentHybridStatus(item, now)).toBe('failed')
  })
  it('does not claim a single probe is 100 percent traffic success', () => {
    expect(hybridSuccess({ ...point, source: 'active' })).toBe('探测成功')
    expect(hybridSuccess({ ...point, source: 'active', reasons: ['probe_failed'] })).toBe('探测失败')
  })
  it('keeps empty minutes rather than compressing time', () => {
    const timeline = hybridTimeline(item, now)
    expect(timeline).toHaveLength(60)
    expect(timeline[0].status).toBe('failed')
    expect(timeline[1].status).toBe('')
    expect(timeline[0].title).toContain('首 Token 过慢')
  })
  it('does not present stale or paused observations as current health', () => {
    expect(currentHybridStatus(item, now + 60000)).toBe('unknown')
    expect(currentHybridStatus({ ...item, rule: { ...item.rule, enabled: false } }, now)).toBe('maintenance')
  })
  it('keeps the latest finalized minute visible after the half-minute watermark', () => {
    const timeline = hybridTimeline(item, Date.parse('2026-09-18T10:02:40Z'))
    expect(timeline[0].checked_at).toBe('2026-09-18T10:01:00.000Z')
    expect(timeline[0].status).toBe('failed')
  })
  it('does not present provisional probe data as current health', () => {
    const provisional = { ...point, minute: '2026-09-18T10:02:00Z', finalized: false }
    const provisionalItem = { ...item, minutes: [provisional, point] }
    expect(currentHybridStatus(provisionalItem, now)).toBe('unknown')
    expect(hybridTimeline(provisionalItem, Date.parse('2026-09-18T10:03:40Z'))[0].status).toBe('')
  })
})
