<template>
  <BaseDialog :show="show" :title="t('admin.activeRequests.title')" width="wide" :close-on-click-outside="true" @close="$emit('close')">
    <div v-if="target" class="space-y-4">
      <!-- Header: who we are looking at + live counters -->
      <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <div class="flex items-center gap-3">
          <div class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-full bg-primary-100 dark:bg-primary-900/30">
            <PlatformIcon v-if="mode === 'account' && target.platform" :platform="asPlatform(target.platform)" size="md" />
            <span v-else class="text-lg font-medium text-primary-700 dark:text-primary-300">
              {{ target.title.charAt(0).toUpperCase() }}
            </span>
          </div>
          <div class="min-w-0 flex-1">
            <p class="truncate font-medium text-gray-900 dark:text-white">{{ target.title }}</p>
            <p class="text-xs text-gray-400 dark:text-dark-500">
              {{ mode === 'account' ? t('admin.activeRequests.accountSubtitle', { id: target.id }) : t('admin.activeRequests.userSubtitle', { id: target.id }) }}
            </p>
          </div>
          <div class="flex flex-shrink-0 items-center gap-5 text-right">
            <div>
              <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.activeRequests.concurrency') }}</p>
              <p class="font-mono text-xl font-bold" :class="concurrencyTextClass">
                {{ data?.current ?? 0 }}<span class="text-gray-400 dark:text-gray-500"> / </span>{{ maxLabel }}
              </p>
            </div>
            <div>
              <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('admin.activeRequests.waiting') }}</p>
              <p class="font-mono text-xl font-bold" :class="(data?.waiting ?? 0) > 0 ? 'text-orange-600 dark:text-orange-400' : 'text-gray-900 dark:text-white'">
                {{ data?.waiting ?? 0 }}
              </p>
            </div>
          </div>
        </div>
      </div>

      <!-- Summary chips + refresh controls -->
      <div class="flex flex-wrap items-center gap-2">
        <span v-if="summary.length > 0" class="text-xs font-medium text-gray-500 dark:text-dark-400">
          {{ mode === 'account' ? t('admin.activeRequests.byUser') : t('admin.activeRequests.byGroup') }}
        </span>
        <span
          v-for="chip in summary"
          :key="chip.key"
          class="inline-flex items-center gap-1 rounded-md bg-gray-100 px-2 py-0.5 text-xs font-medium text-gray-700 dark:bg-dark-600 dark:text-gray-300"
        >
          <span class="max-w-48 truncate">{{ chip.label }}</span>
          <span class="font-mono text-gray-400 dark:text-gray-500">×{{ chip.count }}</span>
        </span>
        <div class="ml-auto flex items-center gap-2">
          <AutoRefreshButton
            :enabled="autoRefresh.enabled.value"
            :interval-seconds="autoRefresh.intervalSeconds.value"
            :countdown="autoRefresh.countdown.value"
            :intervals="autoRefresh.intervals"
            @update:enabled="autoRefresh.setEnabled"
            @update:interval="autoRefresh.setInterval"
          />
          <button
            type="button"
            class="inline-flex items-center gap-1.5 rounded-lg border border-gray-200 bg-white px-2.5 py-1.5 text-xs font-medium text-gray-700 shadow-sm transition-colors hover:bg-gray-50 disabled:opacity-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700"
            :disabled="loading"
            @click="load()"
          >
            <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            {{ t('admin.activeRequests.refresh') }}
          </button>
        </div>
      </div>

      <p v-if="loadError && data" class="text-xs text-orange-600 dark:text-orange-400">{{ t('admin.activeRequests.staleData') }}</p>

      <!-- Requests table -->
      <div v-if="loading && !data" class="py-10 text-center text-sm text-gray-500 dark:text-dark-400">{{ t('common.loading') }}</div>
      <div v-else-if="loadError && !data" class="py-10 text-center text-sm text-red-600 dark:text-red-400">
        {{ t('admin.activeRequests.loadFailed') }}
      </div>
      <div v-else-if="rows.length === 0" class="py-10 text-center text-sm text-gray-500 dark:text-dark-400">
        {{ t('admin.activeRequests.empty') }}
      </div>
      <div v-else class="max-h-[50vh] overflow-auto">
        <table class="min-w-full text-sm">
          <thead>
            <tr class="border-b border-gray-200 text-gray-700 dark:border-dark-700 dark:text-gray-300">
              <th v-if="mode === 'account'" class="px-3 py-2 text-left font-medium">{{ t('admin.activeRequests.columns.user') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.activeRequests.columns.group') }}</th>
              <th v-if="mode === 'user'" class="px-3 py-2 text-left font-medium">{{ t('admin.activeRequests.columns.key') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.activeRequests.columns.model') }}</th>
              <th v-if="mode === 'user'" class="px-3 py-2 text-left font-medium">{{ t('admin.activeRequests.columns.account') }}</th>
              <th class="px-3 py-2 text-right font-medium">{{ t('admin.activeRequests.columns.elapsed') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="row in rows"
              :key="row.slot_id"
              class="border-b border-gray-100 dark:border-dark-800"
              :title="row.request_id ? t('admin.activeRequests.requestId', { id: row.request_id }) : undefined"
            >
              <template v-if="row.recorded">
                <td v-if="mode === 'account'" class="px-3 py-2">
                  <p class="max-w-56 truncate text-gray-900 dark:text-white">{{ userLabel(row) }}</p>
                  <p v-if="row.api_key_id" class="max-w-56 truncate text-xs text-gray-400 dark:text-dark-500">
                    {{ keyLabel(row) }}
                  </p>
                </td>
                <td class="px-3 py-2">
                  <GroupBadge
                    v-if="row.group_id"
                    :name="row.group_name || `#${row.group_id}`"
                    :platform="asPlatform(row.group_platform)"
                    :show-rate="false"
                  />
                  <span v-else class="text-xs text-gray-400 dark:text-dark-500">{{ t('admin.activeRequests.noGroup') }}</span>
                </td>
                <td v-if="mode === 'user'" class="px-3 py-2">
                  <span class="block max-w-40 truncate text-gray-700 dark:text-gray-300">{{ row.api_key_id ? keyLabel(row) : '-' }}</span>
                </td>
                <td class="px-3 py-2">
                  <span class="font-mono text-xs text-gray-700 dark:text-gray-300">{{ row.model || '-' }}</span>
                </td>
                <td v-if="mode === 'user'" class="px-3 py-2">
                  <span v-if="row.account_id" class="flex max-w-48 items-center gap-1.5 text-gray-700 dark:text-gray-300">
                    <PlatformIcon v-if="row.account_platform" :platform="asPlatform(row.account_platform)" size="xs" />
                    <span class="truncate">{{ row.account_name || `#${row.account_id}` }}</span>
                  </span>
                  <span v-else class="cursor-help text-xs text-gray-400 dark:text-dark-500" :title="t('admin.activeRequests.noAccountHint')">
                    {{ t('admin.activeRequests.noAccount') }}
                  </span>
                </td>
              </template>
              <td v-else :colspan="detailColumnCount" class="px-3 py-2 text-xs italic text-gray-400 dark:text-dark-500">
                {{ row.live ? t('admin.activeRequests.liveSession') : t('admin.activeRequests.unrecorded') }}
              </td>
              <!-- Live 租约会定期续租，后端给不出会话时长 -->
              <td v-if="row.live" class="px-3 py-2 text-right font-mono text-xs text-gray-400 dark:text-dark-500">-</td>
              <td v-else class="whitespace-nowrap px-3 py-2 text-right font-mono text-xs" :class="elapsedClass(elapsedOf(row))">
                {{ formatElapsed(elapsedOf(row)) }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <p v-if="hasUnrecorded" class="text-xs text-gray-400 dark:text-dark-500">{{ t('admin.activeRequests.unrecordedHint') }}</p>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import { useAutoRefresh } from '@/composables/useAutoRefresh'
import type { ActiveRequestItem, ActiveRequestsResponse, ActiveRequestsTarget, GroupPlatform } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AutoRefreshButton from '@/components/common/AutoRefreshButton.vue'
import GroupBadge from '@/components/common/GroupBadge.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  show: boolean
  mode: 'user' | 'account'
  target: ActiveRequestsTarget | null
}>()
defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const appStore = useAppStore()

const data = ref<ActiveRequestsResponse | null>(null)
const loading = ref(false)
// 最近一次加载失败：没有数据时显示错误而不是"没有请求"，有旧数据时提示数据已过期。
const loadError = ref(false)
// 两次刷新之间本地推进"已运行"时长：fetchedAt 是拿到数据的时刻，nowMs 每秒更新。
const fetchedAt = ref(0)
const nowMs = ref(Date.now())
let clockTimer: number | undefined
let loadSeq = 0

const autoRefresh = useAutoRefresh({
  storageKey: 'admin-active-requests-auto-refresh',
  intervals: [5, 10, 30] as const,
  defaultInterval: 5,
  defaultEnabled: true,
  onRefresh: () => load(true),
  shouldPause: () => !props.show || document.hidden || loading.value,
})

const rows = computed<ActiveRequestItem[]>(() => data.value?.items ?? [])
const hasUnrecorded = computed(() => rows.value.some((row) => !row.recorded && !row.live))
const detailColumnCount = computed(() => (props.mode === 'user' ? 4 : 3))

const maxLabel = computed(() => {
  const max = data.value?.max ?? 0
  return max > 0 ? String(max) : t('admin.activeRequests.unlimited')
})

const concurrencyTextClass = computed(() => {
  const current = data.value?.current ?? 0
  const max = data.value?.max ?? 0
  if (max > 0 && current >= max) return 'text-red-600 dark:text-red-400'
  if (current > 0) return 'text-yellow-600 dark:text-yellow-400'
  return 'text-gray-900 dark:text-white'
})

interface SummaryChip {
  key: string
  label: string
  count: number
}

// 账号视角按用户汇总，用户视角按分组汇总；未记录详情的槽位单独计一项。
const summary = computed<SummaryChip[]>(() => {
  const chips = new Map<string, SummaryChip>()
  for (const row of rows.value) {
    let key: string
    let label: string
    if (!row.recorded) {
      key = row.live ? 'live' : 'unrecorded'
      label = row.live ? t('admin.activeRequests.liveSession') : t('admin.activeRequests.unrecorded')
    } else if (props.mode === 'account') {
      key = `u${row.user_id ?? 0}`
      label = userLabel(row)
    } else {
      key = `g${row.group_id ?? 0}`
      label = row.group_id ? row.group_name || `#${row.group_id}` : t('admin.activeRequests.noGroup')
    }
    const chip = chips.get(key)
    if (chip) chip.count += 1
    else chips.set(key, { key, label, count: 1 })
  }
  return [...chips.values()].sort((a, b) => b.count - a.count)
})

// 后端返回的平台是普通字符串，交给图标组件前收窄类型。
function asPlatform(value?: string): GroupPlatform | undefined {
  return (value || undefined) as GroupPlatform | undefined
}

function userLabel(row: ActiveRequestItem): string {
  if (row.user_email) return row.user_email
  return row.user_id ? `#${row.user_id}` : '-'
}

function keyLabel(row: ActiveRequestItem): string {
  return row.api_key_name ? `${row.api_key_name} #${row.api_key_id}` : `#${row.api_key_id}`
}

function elapsedOf(row: ActiveRequestItem): number {
  const drift = fetchedAt.value > 0 ? Math.max(0, Math.floor((nowMs.value - fetchedAt.value) / 1000)) : 0
  return row.elapsed_seconds + drift
}

function formatElapsed(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m${String(seconds % 60).padStart(2, '0')}s`
  return `${Math.floor(minutes / 60)}h${String(minutes % 60).padStart(2, '0')}m`
}

function elapsedClass(seconds: number): string {
  if (seconds >= 900) return 'text-red-600 dark:text-red-400'
  if (seconds >= 300) return 'text-orange-600 dark:text-orange-400'
  return 'text-gray-700 dark:text-gray-300'
}

async function load(silent = false) {
  const target = props.target
  if (!target) return
  const seq = ++loadSeq
  loading.value = true
  try {
    const result = props.mode === 'account'
      ? await adminAPI.accounts.getActiveRequests(target.id)
      : await adminAPI.users.getActiveRequests(target.id)
    // 弹窗切换目标或关闭后，丢弃过期响应。
    if (seq !== loadSeq || !props.show) return
    data.value = result
    loadError.value = false
    fetchedAt.value = Date.now()
    nowMs.value = fetchedAt.value
    autoRefresh.resetCountdown()
  } catch (e: any) {
    if (seq !== loadSeq || !props.show) return
    loadError.value = true
    if (!silent) {
      appStore.showError(e?.response?.data?.message || t('admin.activeRequests.loadFailed'))
    }
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function startClock() {
  stopClock()
  clockTimer = window.setInterval(() => { nowMs.value = Date.now() }, 1000)
}

function stopClock() {
  if (clockTimer !== undefined) {
    window.clearInterval(clockTimer)
    clockTimer = undefined
  }
}

watch(
  () => [props.show, props.target?.id, props.mode] as const,
  ([show]) => {
    loadSeq += 1
    data.value = null
    loadError.value = false
    fetchedAt.value = 0
    if (!show || !props.target) {
      loading.value = false
      autoRefresh.stop()
      stopClock()
      return
    }
    void load()
    startClock()
    if (autoRefresh.enabled.value) autoRefresh.start()
  },
  { immediate: true }
)

onBeforeUnmount(stopClock)
</script>
