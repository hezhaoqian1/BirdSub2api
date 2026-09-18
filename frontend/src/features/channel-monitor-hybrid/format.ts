import type { HybridItem, HybridMinute } from '@/api/channelMonitorHybrid'
import type { MonitorTimelinePoint } from '@/api/channelMonitor'

export function currentHybridStatus(item: HybridItem, now = Date.now()): string {
  if (!item.rule.enabled) return 'maintenance'
  const point = item.minutes[0]
  const latestExpectedMinute = Math.floor((now - 30000) / 60000) * 60000 - 60000
  if (!point || Date.parse(point.minute) < latestExpectedMinute || !point.finalized) return 'unknown'
  return point.status
}

export function hybridStatusLabel(status: string): string {
  return ({ operational: '运行正常', degraded: '服务降级', failed: '服务异常', error: '服务异常', unknown: '状态待确认', maintenance: '维护中' } as Record<string, string>)[status] || '状态待确认'
}

export function hybridReason(point?: HybridMinute): string {
  if (!point) return '等待首次检测'
  const labels: Record<string, string> = { success_rate: '成功率下降', ttft: '首 Token 过慢', probe_failed: '主动探测失败', monitor_unavailable: '监控数据不可用', no_eligible_requests: '等待主动检测', waiting_request_completion: '等待业务请求完成' }
  return point.reasons?.map(reason => labels[reason] || reason).join(' · ') || (point.status === 'operational' ? '各项指标正常' : '等待有效数据')
}

export function hybridTime(value: string): string {
  return new Date(value).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false })
}

export function hybridTTFT(value: number | null | undefined): string {
  return value == null ? '—' : `${(value / 1000).toFixed(1)}`
}

export function hybridSuccess(point?: HybridMinute): string {
  if (!point) return '—'
  if (point.source === 'active') return point.reasons?.includes('probe_failed') ? '探测失败' : point.status === 'unknown' ? '待确认' : '探测成功'
  return point.success_rate == null ? '—' : `${(point.success_rate * 100).toFixed(1)}%`
}

export function hybridTimeline(item: HybridItem, now = Date.now()): MonitorTimelinePoint[] {
  const end = Math.floor((now - 30000) / 60000) * 60000 - 60000
  const points = new Map(item.minutes.map(point => [Date.parse(point.minute), point]))
  return Array.from({ length: 60 }, (_, index) => {
    const timestamp = end - index * 60000
    const point = points.get(timestamp)
    return {
      status: (!point?.finalized || point.status === 'unknown' ? '' : point.status || '') as MonitorTimelinePoint['status'],
      latency_ms: point?.ttft_ms ?? null,
      ping_latency_ms: null,
      checked_at: new Date(timestamp).toISOString(),
      title: point ? `${hybridTime(point.minute)} · ${point.finalized ? (point.source === 'active' ? '主动探测' : '真实请求统计') : '检测中'} · ${hybridSuccess(point)} · 首 Token ${hybridTTFT(point.ttft_ms)} 秒 · ${hybridReason(point)}` : `${hybridTime(new Date(timestamp).toISOString())} · 暂无数据`,
    }
  })
}
