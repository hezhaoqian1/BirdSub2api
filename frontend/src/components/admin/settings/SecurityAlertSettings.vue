<template>
  <section class="card space-y-5 p-6" :aria-label="t('securityAlerts.title')">
    <div>
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('securityAlerts.title') }}</h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('securityAlerts.description') }}</p>
    </div>
    <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    <p v-if="notice" role="status" class="text-sm text-green-600">{{ notice }}</p>
    <p v-if="loading && !form" class="text-sm text-gray-500">{{ t('securityAlerts.loading') }}</p>

    <template v-if="form && view">
      <div class="flex flex-wrap items-center gap-3 text-sm">
        <span class="text-gray-500 dark:text-gray-400">{{ t('securityAlerts.robot') }}</span>
        <code v-if="view.wecom_webhook_configured" class="break-all">{{ view.wecom_webhook_masked }}</code>
        <span v-else class="text-amber-600 dark:text-amber-400">{{ t('securityAlerts.robotMissing') }}</span>
        <button v-if="view.wecom_webhook_configured" type="button" data-test="send-test" class="btn btn-secondary btn-sm" :disabled="busy" @click="sendTest">{{ t('securityAlerts.sendTest') }}</button>
      </div>
      <label class="flex items-center gap-2 text-sm font-medium"><input v-model="form.enabled" type="checkbox" />{{ t('securityAlerts.enabled') }}</label>

      <fieldset class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
        <legend class="px-1 font-medium">{{ t('securityAlerts.channelTitle') }}</legend>
        <div class="grid gap-4 sm:grid-cols-2">
          <label v-for="field in numberFields" :key="field.key" class="block text-sm">
            <span class="font-medium text-gray-900 dark:text-white">{{ t(field.label) }}</span>
            <input v-model.number="form[field.key]" type="number" min="0" :step="field.step" class="input mt-1" :data-test="field.key" />
            <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">{{ t(field.hint) }}</span>
          </label>
        </div>
        <label class="flex items-start gap-2 text-sm">
          <input v-model="form.channel_quota_error_notify" type="checkbox" class="mt-1" />
          <span>
            <span class="font-medium text-gray-900 dark:text-white">{{ t('securityAlerts.quotaErrorNotify') }}</span>
            <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('securityAlerts.quotaErrorHint') }}</span>
          </span>
        </label>
      </fieldset>

      <fieldset class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-700">
        <legend class="px-1 font-medium">{{ t('securityAlerts.usersTitle') }}</legend>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('securityAlerts.usersHint') }}</p>
        <div class="relative">
          <input v-model="query" type="text" autocomplete="off" class="input" data-test="user-search" :placeholder="t('securityAlerts.searchPlaceholder')" @input="scheduleSearch" />
          <ul v-if="query.trim() && (searching || searched)" class="absolute z-20 mt-1 max-h-60 w-full overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-700">
            <li v-if="searching" class="px-3 py-2 text-sm text-gray-500">{{ t('common.loading') }}</li>
            <li v-else-if="!results.length" class="px-3 py-2 text-sm text-gray-500">{{ t('securityAlerts.searchEmpty') }}</li>
            <li v-for="user in searching ? [] : results" :key="user.id">
              <button type="button" class="flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-sm hover:bg-gray-50 disabled:opacity-50 dark:hover:bg-dark-600" :disabled="isWatched(user.id)" :data-test="`add-user-${user.id}`" @click="addUser(user)">
                <span class="truncate">#{{ user.id }} {{ user.email || user.username }}</span>
                <span class="shrink-0 text-gray-500">{{ isWatched(user.id) ? t('securityAlerts.alreadyWatched') : money(user.balance) }}</span>
              </button>
            </li>
          </ul>
        </div>
        <p v-if="!form.user_balance_watches.length" class="text-sm text-gray-500">{{ t('securityAlerts.noWatches') }}</p>
        <div v-else class="overflow-x-auto">
          <table class="w-full min-w-[560px] text-sm">
            <thead>
              <tr class="text-left text-xs text-gray-500 dark:text-gray-400">
                <th class="py-2 pr-3 font-medium">{{ t('securityAlerts.colUser') }}</th>
                <th class="py-2 pr-3 font-medium">{{ t('securityAlerts.colBalance') }}</th>
                <th class="py-2 pr-3 font-medium">{{ t('securityAlerts.colThreshold') }}</th>
                <th class="py-2 pr-3 font-medium">{{ t('securityAlerts.colNote') }}</th>
                <th class="py-2" />
              </tr>
            </thead>
            <tbody>
              <tr v-for="(watch, index) in form.user_balance_watches" :key="watch.user_id" class="border-t border-gray-100 dark:border-dark-700">
                <td class="py-2 pr-3">
                  <span class="font-medium">#{{ watch.user_id }}</span>
                  <span class="ml-1 text-gray-600 dark:text-gray-300">{{ userInfo[watch.user_id]?.email || '' }}</span>
                  <span v-if="userInfo[watch.user_id] && !userInfo[watch.user_id].found" class="ml-1 text-xs text-red-600">{{ t('securityAlerts.userMissing') }}</span>
                </td>
                <td class="py-2 pr-3 whitespace-nowrap">
                  <template v-if="userInfo[watch.user_id]?.found">
                    {{ money(userInfo[watch.user_id].balance) }}
                    <span v-if="userInfo[watch.user_id].balance < watch.threshold" class="ml-1 rounded bg-amber-100 px-1.5 py-0.5 text-xs text-amber-800 dark:bg-amber-900/30 dark:text-amber-200">{{ t('securityAlerts.belowBadge') }}</span>
                  </template>
                  <span v-if="!savedWatchIds.has(watch.user_id)" class="ml-1 text-xs text-gray-500">{{ t('securityAlerts.pendingSave') }}</span>
                </td>
                <td class="py-2 pr-3"><input v-model.number="watch.threshold" type="number" min="0" step="1" class="input w-28" :data-test="`threshold-${watch.user_id}`" /></td>
                <td class="py-2 pr-3"><input v-model="watch.note" type="text" maxlength="60" class="input" /></td>
                <td class="py-2 text-right"><button type="button" class="btn btn-secondary btn-sm text-red-600" @click="form.user_balance_watches.splice(index, 1)">{{ t('securityAlerts.remove') }}</button></td>
              </tr>
            </tbody>
          </table>
        </div>
      </fieldset>

      <div class="flex gap-2">
        <button type="button" data-test="save" class="btn btn-primary" :disabled="busy" @click="save">{{ busy ? t('securityAlerts.saving') : t('securityAlerts.save') }}</button>
        <button type="button" class="btn btn-secondary" :disabled="busy || loading" @click="load">{{ t('securityAlerts.reload') }}</button>
      </div>
    </template>
    <TotpStepUpDialog :controller="stepUp" />
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import * as api from '@/api/admin/securityAlerts'
import type { SecurityAlertConfig, SecurityAlertConfigView } from '@/api/admin/securityAlerts'
import { getById as getUserById, list as listUsers } from '@/api/admin/users'
import type { AdminUser } from '@/types'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'
import { isStepUpCancelled, useStepUp } from '@/composables/useStepUp'

type NumberKey = 'channel_spend_floor' | 'channel_spend_multiplier' | 'channel_balance_check_minutes' |
  'channel_low_balance_usd' | 'channel_low_balance_cny' | 'channel_runway_hours'
// 只提交本卡片管理的字段；其余安全规则由服务端按原值合并保留。
type EditableConfig = Pick<SecurityAlertConfig, NumberKey | 'enabled' | 'channel_quota_error_notify' | 'user_balance_watches'>
interface UserInfo { email: string; balance: number; found: boolean }

const numberFields: { key: NumberKey; label: string; hint: string; step: number }[] = [
  { key: 'channel_spend_floor', label: 'securityAlerts.spendFloor', hint: 'securityAlerts.spendFloorHint', step: 10 },
  { key: 'channel_spend_multiplier', label: 'securityAlerts.spendMultiplier', hint: 'securityAlerts.spendMultiplierHint', step: 0.5 },
  { key: 'channel_balance_check_minutes', label: 'securityAlerts.balanceInterval', hint: 'securityAlerts.balanceIntervalHint', step: 5 },
  { key: 'channel_runway_hours', label: 'securityAlerts.runwayHours', hint: 'securityAlerts.runwayHint', step: 1 },
  { key: 'channel_low_balance_usd', label: 'securityAlerts.lowUSD', hint: 'securityAlerts.lowHint', step: 5 },
  { key: 'channel_low_balance_cny', label: 'securityAlerts.lowCNY', hint: 'securityAlerts.lowHint', step: 10 },
]

const { t } = useI18n()
const stepUp = useStepUp()
const view = ref<SecurityAlertConfigView | null>(null)
const form = ref<EditableConfig | null>(null)
const userInfo = ref<Record<number, UserInfo>>({})
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const query = ref('')
const results = ref<AdminUser[]>([])
const searching = ref(false)
const searched = ref(false)
let searchTimer: ReturnType<typeof setTimeout> | undefined
let searchSeq = 0

const savedWatchIds = computed(() => new Set((view.value?.config.user_balance_watches ?? []).map(w => w.user_id)))

function report(e: unknown) {
  if (!isStepUpCancelled(e)) error.value = (e as { message?: string })?.message || t('securityAlerts.operationFailed')
}

function apply(next: SecurityAlertConfigView) {
  view.value = next
  const c = next.config
  form.value = {
    enabled: c.enabled,
    channel_spend_floor: c.channel_spend_floor,
    channel_spend_multiplier: c.channel_spend_multiplier,
    channel_balance_check_minutes: c.channel_balance_check_minutes,
    channel_low_balance_usd: c.channel_low_balance_usd,
    channel_low_balance_cny: c.channel_low_balance_cny,
    channel_runway_hours: c.channel_runway_hours,
    channel_quota_error_notify: c.channel_quota_error_notify,
    user_balance_watches: (c.user_balance_watches ?? []).map(w => ({ ...w, note: w.note ?? '' })),
  }
  const info: Record<number, UserInfo> = {}
  for (const st of next.user_watch_status ?? []) info[st.user_id] = { email: st.email || st.username, balance: st.balance, found: st.found }
  userInfo.value = info
}

async function load() {
  loading.value = true
  error.value = ''
  try { apply(await api.getConfig()) } catch (e) { report(e) } finally { loading.value = false }
}

async function save() {
  if (!form.value) return
  notice.value = ''
  if (form.value.user_balance_watches.some(w => !(Number(w.threshold) > 0))) { error.value = t('securityAlerts.thresholdInvalid'); return }
  const patch: EditableConfig = JSON.parse(JSON.stringify(form.value))
  for (const field of numberFields) patch[field.key] = Number(patch[field.key]) || 0
  patch.user_balance_watches = patch.user_balance_watches.map(w => ({ user_id: w.user_id, threshold: Number(w.threshold), note: (w.note ?? '').trim() }))
  busy.value = true
  error.value = ''
  try {
    await stepUp.run(async () => { apply(await api.updateConfig(patch)) })
    notice.value = t('securityAlerts.saved')
  } catch (e) { report(e) } finally { busy.value = false }
}

async function sendTest() {
  busy.value = true
  error.value = ''
  notice.value = ''
  try { await api.sendTest(); notice.value = t('securityAlerts.testSent') } catch (e) { report(e) } finally { busy.value = false }
}

// 站内余额统一按美元两位小数显示，与后端告警文案一致。
function money(value: number | null | undefined) {
  const v = Number(value) || 0
  return (v < 0 ? '-$' : '$') + Math.abs(v).toFixed(2)
}

function isWatched(id: number) {
  return !!form.value?.user_balance_watches.some(w => w.user_id === id)
}

function addUser(user: AdminUser) {
  if (!form.value || isWatched(user.id)) return
  // 默认提醒线取当前余额的一成（至少 $10），保存前可以修改。
  const threshold = Math.max(10, Math.round((user.balance || 0) * 0.1))
  form.value.user_balance_watches.push({ user_id: user.id, threshold, note: '' })
  userInfo.value = { ...userInfo.value, [user.id]: { email: user.email || user.username, balance: user.balance, found: true } }
  query.value = ''
  results.value = []
  searched.value = false
}

function scheduleSearch() {
  if (searchTimer) clearTimeout(searchTimer)
  searched.value = false
  searchTimer = setTimeout(runSearch, 300)
}

async function runSearch() {
  const q = query.value.trim()
  const seq = ++searchSeq
  if (!q) { results.value = []; return }
  searching.value = true
  try {
    let found: AdminUser[] = []
    if (/^\d+$/.test(q)) {
      try { found = [await getUserById(Number(q))] } catch { found = [] }
    }
    if (!found.length) found = (await listUsers(1, 8, { search: q })).items ?? []
    if (seq === searchSeq) results.value = found
  } catch (e) {
    if (seq === searchSeq) { results.value = []; report(e) }
  } finally {
    if (seq === searchSeq) { searching.value = false; searched.value = true }
  }
}

onMounted(load)
onBeforeUnmount(() => { if (searchTimer) clearTimeout(searchTimer) })
</script>
