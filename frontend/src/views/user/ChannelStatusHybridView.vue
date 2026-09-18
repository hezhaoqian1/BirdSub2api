<template>
  <AppLayout>
    <div class="mx-auto max-w-7xl space-y-6">
      <header class="rounded-3xl border border-gray-200/70 bg-white p-6 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-8">
        <div class="flex flex-wrap items-center justify-between gap-4">
          <div><div class="mb-3 flex items-center gap-2 text-xs font-medium text-blue-600"><span class="h-2 w-2 rounded-full bg-blue-500"></span> SERVICE STATUS <span v-if="demo" class="rounded bg-amber-50 px-2 py-0.5 text-amber-700">本地演示数据</span></div><h1 class="text-2xl font-bold tracking-tight text-gray-900 dark:text-white">服务状态</h1><p class="mt-2 text-sm text-gray-500">每一分钟，了解模型的可用性与响应速度。</p></div>
          <button class="btn btn-secondary text-xs" :disabled="loading" @click="refresh">{{ loading ? '更新中…' : '刷新状态' }}</button>
        </div>
        <div class="mt-6 flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-5 dark:border-dark-700"><div class="flex items-center gap-2 text-sm font-medium" :class="issueCount ? 'text-amber-600' : 'text-emerald-600'"><span class="h-2 w-2 rounded-full" :class="issueCount ? 'bg-amber-500' : 'bg-emerald-500'"></span>{{ issueCount ? `${issueCount} 项服务需要关注` : items.length ? '当前服务运行正常' : '等待监控数据' }}</div><span class="text-xs text-gray-400">每分钟评估 · 真实请求优先，空闲时自动检测</span></div>
      </header>
      <div v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-sm text-red-600">{{ error }}</div>
      <div class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-sm font-semibold text-gray-800 dark:text-gray-200">模型服务 <span class="ml-2 font-normal text-gray-400">{{ items.length }}</span></h2><div class="flex gap-4 text-xs text-gray-500"><span><i class="mr-1.5 inline-block h-2 w-2 rounded-full bg-emerald-500"></i>正常</span><span><i class="mr-1.5 inline-block h-2 w-2 rounded-full bg-amber-500"></i>降级</span><span><i class="mr-1.5 inline-block h-2 w-2 rounded-full bg-red-500"></i>异常</span><span><i class="mr-1.5 inline-block h-2 w-2 rounded-full bg-gray-300"></i>待确认</span></div></div>
      <div class="grid grid-cols-1 gap-5 md:grid-cols-2 xl:grid-cols-3"><HybridMonitorCard v-for="item in items" :key="item.rule.id" :item="item" :now="now" /></div>
      <div v-if="!loading && !items.length" class="rounded-2xl border border-dashed border-gray-200 p-12 text-center text-sm text-gray-400">暂无对你可见的监控服务</div>
      <p class="pb-4 text-center text-xs leading-6 text-gray-400">首 Token 表示开始收到有效回复所需的时间。<br>真实请求展示分钟统计；空闲时展示单次探测结果，两者不会混算成功率。</p>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import AppLayout from '@/components/layout/AppLayout.vue'
import { getHybridSnapshot, type HybridItem } from '@/api/channelMonitorHybrid'
import HybridMonitorCard from '@/features/channel-monitor-hybrid/HybridMonitorCard.vue'
import { currentHybridStatus } from '@/features/channel-monitor-hybrid/format'
const props = defineProps<{ initial?: HybridItem[] }>()
const items = ref<HybridItem[]>(props.initial || [])
const loading = ref(false)
const error = ref('')
const now = ref(Date.now())
const demo = import.meta.env.DEV && import.meta.env.VITE_HYBRID_DEMO === '1'
const issueCount = computed(() => items.value.filter(item => !['operational', 'maintenance'].includes(currentHybridStatus(item, now.value))).length)
const emit = defineEmits<{ disabled: [] }>()
async function refresh() { loading.value = true; try { const data = await getHybridSnapshot(); if (!data.config.enabled) { emit('disabled'); return } items.value = data.items; error.value = '' } catch { error.value = '状态更新失败，已有结果可能过期。请稍后重试。' } finally { loading.value = false } }
let timer: ReturnType<typeof setInterval> | undefined
let nextRefresh = Date.now() + 30000 + Math.random() * 5000
onMounted(() => { if (!props.initial) void refresh(); timer = setInterval(() => { if (document.hidden) return; now.value = Date.now(); if (now.value >= nextRefresh && !loading.value) { nextRefresh = now.value + 30000 + Math.random() * 5000; void refresh() } }, 1000) })
onBeforeUnmount(() => clearInterval(timer))
</script>
