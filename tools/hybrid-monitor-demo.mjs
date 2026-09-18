import http from 'node:http'
import { randomBytes } from 'node:crypto'

const tokens = new Map()
const groups = [{ id: 1, name: '标准对话', platform: 'openai' }, { id: 2, name: '高性能推理', platform: 'openai' }, { id: 3, name: 'Claude 专线', platform: 'anthropic' }]
const names = ['标准对话服务', '深度推理服务', 'Claude 编程服务', '轻量对话服务', '多模态服务', '备用推理服务']
const models = ['gpt-5.4', 'gpt-5.4-thinking', 'claude-sonnet-4-6', 'gpt-5.4-mini', 'gemini-3.1-pro', 'deepseek-reasoner']
let config = { version: 1, enabled: true, notifications_enabled: true, webhook_configured: true, secret_configured: true, rules: names.map((name, index) => ({ id: `demo-${index}`, name, model: models[index], group_id: index % 3 + 1, monitor_id: index + 1, enabled: true, public: true, muted: index === 5, red_success: .6, green_success: .95, warning_ttft_ms: 10000, critical_ttft_ms: 25000, minimum_ttft_samples: 5 })) }
const settings = { site_name: 'Bird API', site_logo: '', registration_enabled: false, email_verify_enabled: false, turnstile_enabled: false, channel_monitor_enabled: true, channel_monitor_mode: 'v1', channel_monitor_hide_throughput: true, available_channels_enabled: false, subscriptions_enabled: false, purchase_subscription_enabled: false, backend_mode_enabled: false, run_mode: 'standard', custom_menu_items: [], captcha_enabled: false, announcement_enabled: false }

function snapshot(admin) {
  const end = Math.floor((Date.now() - 30000) / 60000) * 60000 - 60000
  const items = config.rules.map((rule, index) => {
    const minutes = Array.from({ length: 60 }, (_, offset) => {
      const source = index === 3 || (index === 1 && offset < 2) ? 'active' : 'passive'
      let status = 'operational'
      let reasons = []
      let rate = .98 + (offset % 3) / 100
      let ttft = [2300, 7200, 4800, 1700, 14200, 5100][index] + (offset % 5) * 140
      if (index === 1 && offset < 7) { status = 'failed'; reasons = ['probe_failed']; rate = .4; ttft = null }
      if (index === 2 && offset < 2) { status = 'failed'; reasons = ['ttft']; ttft = 28400; rate = 1 }
      if (index === 4) { status = 'degraded'; reasons = ['ttft'] }
      if (index === 5 && offset > 12 && offset < 19) { status = 'failed'; reasons = ['success_rate']; rate = .35 }
      const requests = source === 'passive' ? 80 + index * 23 + offset % 7 : 0
      return { minute: new Date(end - offset * 60000).toISOString(), observed_at: new Date(end - offset * 60000 + 85000).toISOString(), source, status, reasons, success_rate: source === 'passive' ? rate : null, alert_success_rate: rate, requests, successes: Math.round(requests * rate), failures: requests - Math.round(requests * rate), excluded: 0, ttft_ms: ttft, ttft_samples: source === 'active' ? 1 : requests, ttft_sufficient: ttft !== null, finalized: true, detail: status === 'failed' && reasons.includes('probe_failed') ? '探测返回 HTTP 503' : '' }
    })
    if (!admin) return { rule: { id: rule.id, name: rule.name, model: rule.model, enabled: rule.enabled }, minutes: minutes.map(({ requests, successes, failures, excluded, alert_success_rate, ttft_samples, detail, ...point }) => point) }
    return { rule, minutes, state: { minute: minutes[0].minute, red_count: index === 1 ? 3 : index === 2 ? 2 : 0, green_count: index === 1 || index === 2 ? 0 : 2, incident: index === 1, generation: index === 1 ? 1 : 0, blind_count: 0, blind_spot: false } }
  })
  const notifications = [{ id: 2, rule_id: 'demo-1', minute: new Date(end - 4 * 60000).toISOString(), kind: 'incident', attempts: 1, sent_at: new Date(end - 3 * 60000).toISOString(), last_error: '' }, { id: 1, rule_id: 'demo-5', minute: new Date(end - 11 * 60000).toISOString(), kind: 'recovery', attempts: 1, sent_at: new Date(end - 10 * 60000).toISOString(), last_error: '' }]
  return admin ? { config, items, notifications } : { config: { enabled: config.enabled }, items: items.filter((_, index) => config.rules[index].public) }
}

function user(role) { return { id: role === 'admin' ? 1 : 2, username: role === 'admin' ? '管理员' : '演示用户', email: `${role}@demo.local`, role, status: 'active', balance: 100, concurrency: 5, allowed_groups: [1, 2, 3], balance_notify_enabled: false, balance_notify_threshold: null, balance_notify_extra_emails: [], created_at: new Date().toISOString(), updated_at: new Date().toISOString(), run_mode: 'standard' } }
const server = http.createServer(async (request, response) => {
  const path = new URL(request.url, 'http://localhost').pathname
  const send = (data, status = 200) => { response.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' }); response.end(JSON.stringify({ code: status === 200 ? 0 : status, data, message: status === 200 ? 'OK' : 'Demo request rejected' })) }
  let raw = ''
  for await (const chunk of request) { raw += chunk; if (raw.length > 1000000) return send(null, 413) }
  let body = {}
  try { body = raw ? JSON.parse(raw) : {} } catch { return send(null, 400) }
  if (path === '/api/v1/settings/public') return send(settings)
  if (path === '/setup/status') return send({ needs_setup: false, step: 'complete' })
  if (path === '/api/v1/auth/login') {
    if (!['admin@demo.local', 'user@demo.local'].includes(body.email) || body.password !== 'DemoMonitor2026!') return send(null, 401)
    const role = body.email.startsWith('admin') ? 'admin' : 'user'
    const token = randomBytes(24).toString('hex'); tokens.set(token, role)
    return send({ access_token: token, token_type: 'Bearer', expires_in: 86400, user: user(role) })
  }
  const role = tokens.get(request.headers.authorization?.replace('Bearer ', ''))
  if (!role) return send(null, 401)
  if (path.startsWith('/api/v1/admin/') && role !== 'admin') return send(null, 403)
  if (path === '/api/v1/auth/me') return send(user(role))
  if (path === '/api/v1/auth/logout') return send({})
  if (path === '/api/v1/channel-monitor-hybrid') return send(snapshot(false))
  if (path === '/api/v1/admin/channel-monitor-hybrid') return send(snapshot(true))
  if (path === '/api/v1/admin/channel-monitor-hybrid/config' && request.method === 'PUT') { if (body.version !== config.version) return send(null, 409); config = { ...body, version: config.version + 1, webhook: undefined, secret: undefined }; return send(config) }
  if (path === '/api/v1/admin/channel-monitor-hybrid/test-notification') return send({ sent: true })
  if (path === '/api/v1/admin/groups/all') return send(groups)
  if (path === '/api/v1/admin/channel-monitors') return send({ items: models.map((model, index) => ({ id: index + 1, name: `${names[index]}探测`, provider: index === 2 ? 'anthropic' : 'openai', primary_model: model, extra_models: [], check_mode: 'probe', enabled: true, endpoint: 'https://demo.local', group_name: '演示分组' })), total: 6, page: 1, page_size: 100, pages: 1 })
  if (path === '/api/v1/admin/settings') return send(settings)
  if (path.includes('announcements')) return send([])
  if (path.includes('subscriptions')) return send([])
  return send({ items: [], total: 0 })
})
server.listen(8088, '127.0.0.1', () => console.log('Hybrid mock API: http://127.0.0.1:8088 · admin@demo.local / user@demo.local · password: DemoMonitor2026!'))
