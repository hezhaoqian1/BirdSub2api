<template>
  <article class="hybrid-card group rounded-2xl border border-gray-200/80 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800">
    <div class="flex items-start justify-between gap-3">
      <div class="flex min-w-0 items-center gap-3">
        <div class="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-blue-50 text-sm font-bold text-blue-600 dark:bg-blue-500/10">{{ item.rule.name.slice(0, 1) }}</div>
        <div class="min-w-0"><h3 class="truncate font-semibold text-gray-900 dark:text-white">{{ item.rule.name }}</h3><p class="mt-1 truncate font-mono text-xs text-gray-400">{{ item.rule.model }}</p></div>
      </div>
      <span class="shrink-0 rounded-full px-2.5 py-1 text-[11px] font-medium" :class="badgeClass"><span class="mr-1.5">●</span>{{ hybridStatusLabel(status) }}</span>
    </div>
    <div class="mt-5 grid grid-cols-2 gap-3">
      <div class="rounded-xl bg-gray-50 p-3.5 dark:bg-dark-900/50"><p class="text-[11px] text-gray-500">{{ latest?.source === 'active' ? '主动探测结果' : '最近确认成功率' }}</p><p class="mt-2 font-mono text-2xl font-semibold tracking-tight text-gray-900 dark:text-white">{{ latest ? hybridSuccess(latest) : '—' }}</p></div>
      <div class="rounded-xl bg-gray-50 p-3.5 dark:bg-dark-900/50"><p class="text-[11px] text-gray-500">{{ latest?.source === 'active' ? '探测首 Token' : '最近首 Token · P50' }}</p><p class="mt-2 font-mono text-2xl font-semibold tracking-tight text-gray-900 dark:text-white">{{ hybridTTFT(latest?.ttft_ms) }}<span class="ml-1 text-xs font-normal text-gray-400">秒</span></p></div>
    </div>
    <div class="mt-3 flex items-center justify-between gap-2 text-[11px]"><span :class="status === 'failed' || status === 'error' ? 'text-red-500' : 'text-gray-500'">{{ stale ? (item.minutes[0]?.reasons?.includes('waiting_request_completion') ? '等待业务请求完成' : pending ? '检测中' : '等待新的监控结果') : hybridReason(latest) }}</span><span class="shrink-0 text-gray-400">{{ stale && latest ? '上次确认' : latest?.source === 'active' ? '主动探测' : latest ? '真实请求' : '等待采集' }}<template v-if="latest"> · {{ hybridTime(latest.observed_at) }}</template></span></div>
    <MonitorTimeline :buckets="timeline" :countdown-seconds="60 - Math.floor(now / 1000) % 60" :maintenance="!item.rule.enabled" minute-mode />
    <div v-if="admin" class="mt-4 border-t border-gray-100 pt-3 text-xs dark:border-dark-700">
      <div class="flex justify-between text-gray-500"><span>分组 #{{ item.rule.group_id }} · 配置 #{{ item.rule.monitor_id }}</span><span>{{ latest?.source === 'passive' ? `${latest.requests || 0} 个请求` : '单次流式检测' }}</span></div>
      <div class="mt-2 flex justify-between"><span class="text-gray-500">{{ item.state?.incident ? `恢复确认 ${item.state.green_count}/2` : `连续异常 ${item.state?.red_count || 0}/3` }}</span><span :class="item.state?.incident ? 'text-red-500' : 'text-gray-400'">{{ item.rule.muted ? '通知已静默' : item.state?.incident ? '故障事件处理中' : '持续观察中' }}</span></div>
      <button class="mt-3 w-full rounded-lg border border-gray-200 py-2 text-xs font-medium text-gray-600 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-700" @click="$emit('inspect', item)">查看证据与规则 →</button>
    </div>
    <p v-else-if="latest?.source === 'passive' && latest.ttft_ms != null && !latest.ttft_sufficient" class="mt-3 text-[11px] text-gray-400">首 Token 样本不足，暂不参与性能判定</p>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { HybridItem } from '@/api/channelMonitorHybrid'
import MonitorTimeline from '@/components/user/monitor/MonitorTimeline.vue'
import { currentHybridStatus, hybridReason, hybridStatusLabel, hybridSuccess, hybridTime, hybridTimeline, hybridTTFT } from './format'
const props = defineProps<{ item: HybridItem; admin?: boolean; now: number }>()
defineEmits<{ inspect: [item: HybridItem] }>()
const pending = computed(() => props.item.minutes[0]?.finalized === false)
const latest = computed(() => props.item.minutes.find(point => point.finalized))
const timeline = computed(() => hybridTimeline(props.item, props.now))
const status = computed(() => currentHybridStatus(props.item, props.now))
const stale = computed(() => status.value === 'unknown' || status.value === 'maintenance')
const badgeClass = computed(() => ({ operational: 'bg-emerald-50 text-emerald-600 dark:bg-emerald-500/10', degraded: 'bg-amber-50 text-amber-600 dark:bg-amber-500/10', failed: 'bg-red-50 text-red-600 dark:bg-red-500/10', error: 'bg-red-50 text-red-600 dark:bg-red-500/10' } as Record<string, string>)[status.value] || 'bg-gray-100 text-gray-500 dark:bg-dark-700')
</script>
