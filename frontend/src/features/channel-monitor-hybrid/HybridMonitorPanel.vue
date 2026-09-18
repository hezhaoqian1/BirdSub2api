<template>
  <div class="space-y-5">
    <div v-if="demo" class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-2.5 text-xs text-amber-800">本地演示环境 · 以下为模拟监控数据，通知不会发送到真实钉钉。</div>
    <div v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-sm text-red-600">{{ error }}</div>
    <div v-if="notice" role="status" class="rounded-xl bg-emerald-50 p-4 text-sm text-emerald-700">{{ notice }}</div>
    <div class="grid grid-cols-2 gap-4 xl:grid-cols-4">
      <div v-for="metric in summary" :key="metric.label" class="rounded-2xl border border-gray-200/80 bg-white p-5 dark:border-dark-700 dark:bg-dark-800"><p class="text-xs text-gray-500">{{ metric.label }}</p><div class="mt-3 flex items-end justify-between"><strong class="font-mono text-3xl font-semibold" :class="metric.color">{{ metric.value }}</strong><span class="text-[11px] text-gray-400">{{ metric.hint }}</span></div></div>
    </div>
    <section class="rounded-2xl border border-gray-200/80 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
      <div class="flex flex-wrap items-center justify-between gap-4"><div><h2 class="text-sm font-semibold text-gray-900 dark:text-white">融合监控</h2><p class="mt-1 text-xs text-gray-500">真实流量优先 · 空闲主动补位 · 连续 3 次异常通知</p></div><div class="flex flex-wrap items-center gap-3"><label class="flex items-center gap-2 text-xs text-gray-600 dark:text-gray-300"><input :checked="config.enabled" type="checkbox" @change="toggleHybrid">启用融合模式</label><button class="btn btn-secondary text-xs" :disabled="busy" @click="refresh()">刷新</button><button class="btn btn-secondary text-xs" @click="showNotifications = true">钉钉设置</button><button class="btn btn-primary text-xs" @click="editRule()">＋ 添加监控</button></div></div>
    </section>
    <div class="grid grid-cols-1 gap-5 lg:grid-cols-2 2xl:grid-cols-3"><HybridMonitorCard v-for="item in items" :key="item.rule.id" :item="item" :now="now" admin @inspect="selected = $event" /></div>
    <div v-if="!items.length && !busy" class="rounded-2xl border border-dashed border-gray-200 py-14 text-center"><p class="font-medium text-gray-700 dark:text-gray-300">还没有融合监控</p><p class="mt-2 text-sm text-gray-400">复用「探测配置」中的模板，绑定真实分组与模型。</p><button class="btn btn-primary mt-5" @click="editRule()">创建第一条监控</button></div>
    <section class="overflow-hidden rounded-2xl border border-gray-200/80 bg-white dark:border-dark-700 dark:bg-dark-800">
      <div class="flex items-center justify-between border-b border-gray-100 px-5 py-4 dark:border-dark-700"><h2 class="text-sm font-semibold text-gray-900 dark:text-white">通知记录</h2><span class="text-xs text-gray-400">故障与恢复 · 最近 50 条</span></div>
      <div class="overflow-x-auto"><table class="w-full whitespace-nowrap text-left text-xs"><thead class="bg-gray-50 text-gray-500 dark:bg-dark-900/50"><tr><th class="px-5 py-3 font-medium">时间</th><th class="px-5 py-3 font-medium">监控对象</th><th class="px-5 py-3 font-medium">事件</th><th class="px-5 py-3 font-medium">发送状态</th><th class="px-5 py-3 font-medium">尝试次数</th></tr></thead><tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="event in notifications" :key="event.id"><td class="px-5 py-4 font-mono text-gray-500">{{ hybridTime(event.minute) }}</td><td class="px-5 py-4 text-gray-800 dark:text-gray-200">{{ event.rule_id === '__monitor__' ? '监控任务' : config.rules.find(rule => rule.id === event.rule_id)?.name || event.rule_id }}</td><td class="px-5 py-4" :class="event.kind.includes('recovery') ? 'text-emerald-600' : 'text-red-500'">{{ notificationLabel(event.kind) }}</td><td class="px-5 py-4 text-gray-500">{{ event.sent_at ? '已送达' : event.last_error || '等待发送' }}</td><td class="px-5 py-4 text-gray-500">{{ event.attempts }}</td></tr><tr v-if="!notifications.length"><td colspan="5" class="px-5 py-8 text-center text-gray-400">暂无通知事件</td></tr></tbody></table></div>
    </section>
    <BaseDialog :show="!!selected" :title="selected?.rule.name || '监控详情'" width="wide" @close="selected = null">
      <template v-if="selected"><div class="flex flex-wrap items-center justify-between gap-3"><div class="text-sm text-gray-500">{{ selected.rule.model }} · 分组 #{{ selected.rule.group_id }}</div><button class="btn btn-secondary text-xs" @click="editRule(selected.rule)">编辑规则</button></div><div class="my-5 rounded-xl bg-gray-50 p-4 text-xs leading-6 text-gray-600 dark:bg-dark-900 dark:text-gray-300">成功率 &lt; {{ selected.rule.red_success * 100 }}% 判红；首 Token ≥ {{ selected.rule.critical_ttft_ms / 1000 }} 秒判红。<br>连续 3 次红色触发通知；连续 2 次绿色确认恢复。{{ selected.rule.muted ? '当前通知已静默。' : '' }}</div><div class="max-h-96 overflow-auto"><table class="w-full text-left text-xs"><thead><tr class="text-gray-400"><th class="py-3">分钟</th><th>来源</th><th>请求 / 失败 / 排除</th><th>首 Token</th><th>判定依据</th></tr></thead><tbody><tr v-for="point in selected.minutes" :key="point.minute" class="border-t border-gray-100 dark:border-dark-700"><td class="py-3 text-gray-500">{{ hybridTime(point.minute) }}</td><td class="text-gray-500">{{ point.source === 'active' ? '探测' : '真实流量' }}</td><td class="font-mono text-gray-500">{{ point.requests || 0 }} / {{ point.failures || 0 }} / {{ point.excluded || 0 }}</td><td class="text-gray-500">{{ hybridTTFT(point.ttft_ms) }}s</td><td :class="point.status === 'failed' || point.status === 'error' ? 'text-red-500' : 'text-gray-500'">{{ hybridReason(point) }}<div v-if="point.detail" class="mt-1 text-[10px]">{{ point.detail }}</div></td></tr></tbody></table></div></template>
    </BaseDialog>
    <BaseDialog :show="!!draft" title="监控规则" width="wide" @close="draft = null">
      <form v-if="draft" class="space-y-5" @submit.prevent="commitRule">
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2"><label class="hybrid-label">公开展示名称<input v-model="draft.name" class="input mt-2" required maxlength="100" placeholder="例如：标准对话服务"></label><label class="hybrid-label">真实业务分组<select v-model.number="draft.group_id" class="input mt-2" required><option :value="0">选择分组</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }} · #{{ group.id }}</option></select></label><label class="hybrid-label">复用 V1 探测配置<select v-model.number="draft.monitor_id" class="input mt-2" required @change="draft.model = targetModels[0] || ''"><option :value="0">选择探测配置</option><option v-for="monitor in monitors" :key="monitor.id" :value="monitor.id">{{ monitor.name }}</option></select></label><label class="hybrid-label">监控模型<select v-model="draft.model" class="input mt-2" required><option v-for="model in targetModels" :key="model">{{ model }}</option></select></label></div>
        <p class="rounded-xl bg-blue-50 p-3 text-xs leading-5 text-blue-700 dark:bg-blue-500/10 dark:text-blue-300">探测配置需使用本站该分组的专用 API Key，地址与系统前端地址同源。该 Key 仅供监控使用，其请求不计入业务成功率。</p>
        <div class="grid grid-cols-2 gap-4"><label class="hybrid-label">成功率红线（比例）<input v-model.number="draft.red_success" class="input mt-2" type="number" min="0.01" max="0.99" step="0.01" required></label><label class="hybrid-label">成功率绿线（比例）<input v-model.number="draft.green_success" class="input mt-2" type="number" min="0.02" max="1" step="0.01" required></label><label class="hybrid-label">首 Token 黄线（毫秒）<input v-model.number="draft.warning_ttft_ms" class="input mt-2" type="number" min="1000" max="39000" step="1000" required></label><label class="hybrid-label">首 Token 红线（毫秒）<input v-model.number="draft.critical_ttft_ms" class="input mt-2" type="number" min="2000" max="40000" step="1000" required></label><label class="hybrid-label">被动首 Token 最少样本<input v-model.number="draft.minimum_ttft_samples" class="input mt-2" type="number" min="1" max="1000" required></label></div>
        <div class="flex flex-wrap gap-5 text-sm text-gray-600 dark:text-gray-300"><label><input v-model="draft.enabled" type="checkbox" class="mr-2">启用监控</label><label><input v-model="draft.public" type="checkbox" class="mr-2">向有分组权限的用户展示</label><label><input v-model="draft.muted" type="checkbox" class="mr-2">静默通知</label></div>
        <div class="flex justify-end gap-3"><button type="button" class="btn btn-secondary" @click="draft = null">取消</button><button class="btn btn-primary" :disabled="busy">保存规则</button></div>
      </form>
    </BaseDialog>
    <BaseDialog :show="showNotifications" title="钉钉通知" @close="showNotifications = false">
      <div class="space-y-5"><label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300"><input v-model="config.notifications_enabled" type="checkbox">开启故障与恢复推送</label><label class="hybrid-label block">机器人 Webhook<input v-model="config.webhook" type="password" autocomplete="new-password" class="input mt-2" :placeholder="config.webhook_configured ? '已配置，留空保留原值' : 'https://oapi.dingtalk.com/robot/send?...'"></label><label class="hybrid-label block">加签密钥<input v-model="config.secret" type="password" autocomplete="new-password" class="input mt-2" :placeholder="config.secret_configured ? '已配置，留空保留原值' : '选填 SEC…'"></label><p class="text-xs leading-6 text-gray-500">凭证加密保存，不返回明文。测试使用已保存的配置，不影响故障计数。</p><div class="flex justify-end gap-3"><button class="btn btn-secondary" :disabled="busy || !config.webhook_configured" @click="test">发送测试</button><button class="btn btn-primary" :disabled="busy" @click="save">保存配置</button></div></div>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getAll } from '@/api/admin/groups'
import { list, type ChannelMonitor } from '@/api/admin/channelMonitor'
import { getHybridSnapshot, saveHybridConfig, testHybridNotification, type HybridConfig, type HybridItem, type HybridNotification, type HybridRule } from '@/api/channelMonitorHybrid'
import HybridMonitorCard from './HybridMonitorCard.vue'
import { currentHybridStatus, hybridReason, hybridTime, hybridTTFT } from './format'
const config = ref<HybridConfig>({ version: 1, enabled: false, notifications_enabled: false, rules: [], webhook_configured: false, secret_configured: false })
const items = ref<HybridItem[]>([])
const notifications = ref<HybridNotification[]>([])
const groups = ref<{ id: number; name: string }[]>([])
const monitors = ref<ChannelMonitor[]>([])
const now = ref(Date.now())
const busy = ref(false)
const error = ref('')
const notice = ref('')
const draft = ref<HybridRule | null>(null)
const selected = ref<HybridItem | null>(null)
const showNotifications = ref(false)
const demo = import.meta.env.DEV && import.meta.env.VITE_HYBRID_DEMO === '1'
const targetModels = computed(() => { const monitor = monitors.value.find(entry => entry.id === draft.value?.monitor_id); return monitor ? [monitor.primary_model, ...(monitor.extra_models || [])] : [] })
const summary = computed(() => [
  { label: '监控服务', value: items.value.length, hint: '按分组与模型', color: 'text-gray-900 dark:text-white' },
  { label: '运行正常', value: items.value.filter(item => currentHybridStatus(item, now.value) === 'operational').length, hint: '实时状态', color: 'text-emerald-600' },
  { label: '需要关注', value: items.value.filter(item => ['failed', 'error', 'degraded', 'unknown'].includes(currentHybridStatus(item, now.value))).length, hint: '降级、异常或待确认', color: 'text-amber-600' },
  { label: '进行中的故障', value: items.value.filter(item => item.state?.incident).length, hint: '连续 3 次确认', color: 'text-red-500' },
])
function message(cause: unknown): string { return cause instanceof Error ? cause.message : '操作失败，请稍后重试' }
function notificationLabel(kind: string): string { return ({ incident: '连续异常告警', recovery: '服务恢复', blind_spot: '监控数据盲区', blind_recovery: '监控数据恢复', monitor_blind_spot: '评估任务中断', monitor_recovery: '评估任务恢复' } as Record<string, string>)[kind] || kind }
async function refresh(updateConfig = true) { busy.value = true; try { const data = await getHybridSnapshot(true); if (updateConfig) config.value = data.config; items.value = data.items; if (selected.value) selected.value = items.value.find(item => item.rule.id === selected.value?.rule.id) || null; notifications.value = data.notifications || []; error.value = '' } catch (cause) { error.value = message(cause) } finally { busy.value = false } }
async function save(): Promise<boolean> { busy.value = true; notice.value = ''; try { config.value = await saveHybridConfig(config.value); notice.value = '监控配置已保存'; await refresh(); return true } catch (cause) { error.value = message(cause); return false } finally { busy.value = false } }
async function toggleHybrid(event: Event) { const input = event.target as HTMLInputElement; const previous = config.value.enabled; config.value.enabled = input.checked; if (!(await save())) config.value.enabled = previous }
function editRule(rule?: HybridRule) { selected.value = null; draft.value = rule ? { ...rule } : { id: crypto.randomUUID(), name: '', group_id: 0, monitor_id: 0, model: '', enabled: true, public: true, muted: false, red_success: 0.6, green_success: 0.95, warning_ttft_ms: 10000, critical_ttft_ms: 25000, minimum_ttft_samples: 5 } }
async function commitRule() { if (!draft.value) return; const before = [...config.value.rules]; const index = before.findIndex(rule => rule.id === draft.value!.id); const next = [...before]; if (index < 0) next.push({ ...draft.value }); else next[index] = { ...draft.value }; config.value.rules = next; if (await save()) draft.value = null; else config.value.rules = before }
async function test() { busy.value = true; try { await testHybridNotification(); notice.value = '测试通知已送达'; error.value = '' } catch (cause) { error.value = message(cause) } finally { busy.value = false } }
let timer: ReturnType<typeof setInterval> | undefined
let nextRefresh = Date.now() + 30000 + Math.random() * 5000
onMounted(async () => { await refresh(); const results = await Promise.allSettled([getAll(), list({ page_size: 100 })]); if (results[0].status === 'fulfilled') groups.value = results[0].value; if (results[1].status === 'fulfilled') monitors.value = results[1].value.items.filter(item => item.check_mode !== 'quota'); timer = setInterval(() => { if (document.hidden) return; now.value = Date.now(); if (now.value >= nextRefresh && !busy.value && !draft.value && !showNotifications.value) { nextRefresh = now.value + 30000 + Math.random() * 5000; void refresh(false) } }, 1000) })
onBeforeUnmount(() => clearInterval(timer))
</script>

<style scoped>
.hybrid-label { @apply text-xs font-medium text-gray-600 dark:text-gray-300; }
</style>
