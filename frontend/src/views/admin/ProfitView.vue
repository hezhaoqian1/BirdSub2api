<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white">{{ t('admin.profit.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.profit.description') }}</p>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <DateRangePicker v-model:start-date="startDate" v-model:end-date="endDate" @change="loadReport" />
          <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <Toggle v-model="excludeAdmin" data-testid="profit-exclude-admin" />
            {{ t('admin.profit.excludeAdmin') }}
          </label>
          <button class="btn btn-secondary" :disabled="loading" @click="loadReport">
            {{ t('common.refresh') }}
          </button>
        </div>
      </div>

      <div v-if="loading && !report" class="flex justify-center py-12">
        <LoadingSpinner />
      </div>

      <template v-else-if="report">
        <!-- 汇总卡片 -->
        <div class="grid grid-cols-2 gap-4 lg:grid-cols-5">
          <div class="card p-4">
            <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.profit.cards.revenue') }}</p>
            <p class="mt-1 text-xl font-bold text-gray-900 dark:text-white" data-testid="profit-revenue">
              {{ formatMoney(report.summary.revenue) }}
            </p>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.profit.cards.requests', { count: formatNumber(report.summary.requests) }) }}
            </p>
          </div>
          <div class="card p-4">
            <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.profit.cards.cost') }}</p>
            <p class="mt-1 text-xl font-bold text-orange-600 dark:text-orange-400">{{ formatMoney(report.summary.cost) }}</p>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.profit.cards.standardCost', { value: formatAmount(report.summary.standard_cost) }) }}
            </p>
          </div>
          <div class="card p-4">
            <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.profit.cards.profit') }}</p>
            <p class="mt-1 text-xl font-bold" :class="profitClass(report.summary.profit)" data-testid="profit-profit">
              {{ formatMoney(report.summary.profit) }}
            </p>
          </div>
          <div class="card p-4">
            <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.profit.cards.margin') }}</p>
            <p class="mt-1 text-xl font-bold" :class="profitClass(report.summary.profit)">
              {{ formatPercent(report.summary.profit_margin) }}
            </p>
          </div>
          <div class="card col-span-2 p-4 lg:col-span-1" :title="t('admin.profit.cards.subscriptionHint')">
            <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('admin.profit.cards.subscription') }}</p>
            <p class="mt-1 text-xl font-bold text-gray-500 dark:text-gray-400">{{ formatMoney(report.subscription.revenue) }}</p>
            <p class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.profit.cards.requests', { count: formatNumber(report.subscription.requests) }) }}
            </p>
          </div>
        </div>

        <div
          v-if="report.unconfigured_revenue > 0"
          class="rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800 dark:border-amber-900/50 dark:bg-amber-900/20 dark:text-amber-300"
          data-testid="profit-unconfigured-warning"
        >
          {{
            t('admin.profit.unconfiguredWarning', {
              revenue: formatAmount(report.unconfigured_revenue),
              percent: formatPercent(report.summary.revenue > 0 ? report.unconfigured_revenue / report.summary.revenue : null)
            })
          }}
        </div>

        <!-- 历史回填 -->
        <div class="card p-4">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="min-w-0 flex-1">
              <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.profit.backfill.title') }}</p>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                <template v-if="report.backfill">
                  {{
                    t('admin.profit.backfill.applied', {
                      appliedAt: formatDateTime(report.backfill.applied_at),
                      start: formatDateTime(report.backfill.start_time),
                      rows: formatNumber(report.backfill.rows)
                    })
                  }}
                </template>
                <template v-else>{{ t('admin.profit.backfill.notApplied') }}</template>
              </p>
            </div>
            <div v-if="!report.backfill" class="flex items-center gap-2">
              <label class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.profit.backfill.startDate') }}</label>
              <input v-model="backfillStart" type="date" class="input w-40" />
              <button class="btn btn-secondary" :disabled="backfillLoading || !backfillStart" @click="openBackfillPreview">
                {{ t('admin.profit.backfill.preview') }}
              </button>
            </div>
          </div>
        </div>

        <!-- 趋势 -->
        <div v-if="trendChartData" class="card p-4">
          <p class="mb-3 text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.profit.trend.title') }}</p>
          <div class="h-64">
            <Line :data="trendChartData" :options="lineOptions" />
          </div>
        </div>

        <!-- 明细 -->
        <div class="card">
          <div class="flex gap-1 overflow-x-auto border-b border-gray-200 px-4 dark:border-dark-700">
            <button
              v-for="tab in tabs"
              :key="tab"
              class="whitespace-nowrap border-b-2 px-3 py-3 text-sm font-medium"
              :class="
                activeTab === tab
                  ? 'border-primary-600 text-primary-600 dark:text-primary-400'
                  : 'border-transparent text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'
              "
              @click="activeTab = tab"
            >
              {{ t(`admin.profit.tabs.${tab}`) }}
            </button>
          </div>

          <div class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="text-left text-xs text-gray-500 dark:text-gray-400">
                  <th class="px-4 py-2">{{ t('admin.profit.columns.name') }}</th>
                  <template v-if="activeTab === 'account'">
                    <th class="px-2 py-2">{{ t('admin.profit.columns.status') }}</th>
                    <th class="px-2 py-2">{{ t('admin.profit.columns.costRate') }}</th>
                    <th class="px-2 py-2">{{ t('admin.profit.columns.probeRate') }}</th>
                  </template>
                  <th class="px-2 py-2 text-right">{{ t('admin.profit.columns.requests') }}</th>
                  <th class="px-2 py-2 text-right">{{ t('admin.profit.columns.standardCost') }}</th>
                  <th class="px-2 py-2 text-right">{{ t('admin.profit.columns.revenue') }}</th>
                  <th class="px-2 py-2 text-right">{{ t('admin.profit.columns.cost') }}</th>
                  <th class="px-2 py-2 text-right">{{ t('admin.profit.columns.profit') }}</th>
                  <th class="px-4 py-2 text-right">{{ t('admin.profit.columns.margin') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-if="tableRows.length === 0">
                  <td colspan="10" class="px-4 py-8 text-center text-gray-500 dark:text-gray-400">{{ t('admin.profit.empty') }}</td>
                </tr>
                <tr v-for="row in tableRows" :key="row.key" class="text-gray-700 dark:text-gray-300">
                  <td class="max-w-[260px] truncate px-4 py-2" :title="row.name">
                    {{ row.name }}
                    <span v-if="row.account?.deleted" class="ml-1 text-xs text-gray-400">({{ t('admin.profit.deleted') }})</span>
                  </td>
                  <template v-if="activeTab === 'account' && row.account">
                    <td class="px-2 py-2">
                      <span class="rounded px-1.5 py-0.5 text-xs" :class="statusClass(row.account.cost_status)">
                        {{ t(`admin.profit.status.${row.account.cost_status}`) }}
                      </span>
                    </td>
                    <td class="px-2 py-2">
                      <div v-if="row.account.cost_status !== 'subscription' && !row.account.deleted" class="flex items-center gap-1">
                        <input
                          v-model="rateDrafts[row.account.account_id]"
                          type="number"
                          min="0"
                          step="0.001"
                          class="input w-20 px-2 py-1 text-xs"
                          :disabled="row.account.rate_sync_enabled"
                          :title="row.account.rate_sync_enabled ? t('admin.profit.rateSynced') : ''"
                          :data-testid="`profit-rate-input-${row.account.account_id}`"
                        />
                        <button
                          v-if="isRateDirty(row.account)"
                          class="btn btn-primary px-2 py-1 text-xs"
                          :disabled="savingRateId === row.account.account_id"
                          @click="saveRate(row.account)"
                        >
                          {{ t('admin.profit.saveRate') }}
                        </button>
                      </div>
                      <span v-else class="font-mono text-xs text-gray-400">—</span>
                    </td>
                    <td class="px-2 py-2">
                      <div v-if="row.account.probe_rate != null" class="flex items-center gap-1">
                        <span class="font-mono text-xs">{{ row.account.probe_rate }}x</span>
                        <button
                          v-if="!row.account.rate_sync_enabled && row.account.probe_rate !== row.account.cost_rate"
                          class="text-xs text-primary-600 hover:underline dark:text-primary-400"
                          @click="rateDrafts[row.account.account_id] = String(row.account.probe_rate)"
                        >
                          {{ t('admin.profit.useProbeRate') }}
                        </button>
                      </div>
                      <span v-else class="text-xs text-gray-400">{{ row.account.probe_status || '—' }}</span>
                    </td>
                  </template>
                  <td class="px-2 py-2 text-right font-mono">{{ formatNumber(row.amounts.requests) }}</td>
                  <td class="px-2 py-2 text-right font-mono text-gray-400">{{ formatMoney(row.amounts.standard_cost) }}</td>
                  <td class="px-2 py-2 text-right font-mono">{{ formatMoney(row.amounts.revenue) }}</td>
                  <td class="px-2 py-2 text-right font-mono">{{ row.subscription ? '—' : formatMoney(row.amounts.cost) }}</td>
                  <td class="px-2 py-2 text-right font-mono" :class="row.subscription ? '' : profitClass(row.amounts.profit)">
                    {{ row.subscription ? '—' : formatMoney(row.amounts.profit) }}
                  </td>
                  <td class="px-4 py-2 text-right font-mono" :class="row.subscription ? '' : profitClass(row.amounts.profit)">
                    {{ row.subscription ? '—' : formatPercent(row.amounts.profit_margin) }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </div>

    <BaseDialog
      :show="backfillDialogOpen"
      :title="t('admin.profit.backfill.previewTitle')"
      width="wide"
      @close="backfillDialogOpen = false"
    >
      <div v-if="backfillPreview" class="space-y-3">
        <p class="text-sm text-gray-600 dark:text-gray-300">
          {{ t('admin.profit.backfill.previewHint', { start: backfillStart }) }}
        </p>
        <p v-if="backfillPreview.candidates.length === 0" class="text-sm text-amber-600 dark:text-amber-400">
          {{ t('admin.profit.backfill.noCandidates') }}
        </p>
        <table v-else class="w-full text-sm">
          <thead>
            <tr class="text-left text-xs text-gray-500 dark:text-gray-400">
              <th class="py-2 pr-2"></th>
              <th class="py-2">{{ t('admin.profit.backfill.account') }}</th>
              <th class="py-2 text-right">{{ t('admin.profit.backfill.rate') }}</th>
              <th class="py-2 text-right">{{ t('admin.profit.backfill.rows') }}</th>
              <th class="py-2 text-right">{{ t('admin.profit.backfill.oldCost') }}</th>
              <th class="py-2 text-right">{{ t('admin.profit.backfill.newCost') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="c in backfillPreview.candidates" :key="c.account_id" class="text-gray-700 dark:text-gray-300">
              <td class="py-2 pr-2">
                <input v-model="backfillSelected" type="checkbox" :value="c.account_id" />
              </td>
              <td class="py-2">{{ c.account_name }}</td>
              <td class="py-2 text-right font-mono">{{ c.rate }}x</td>
              <td class="py-2 text-right font-mono">{{ formatNumber(c.rows) }}</td>
              <td class="py-2 text-right font-mono">{{ formatMoney(c.old_cost) }}</td>
              <td class="py-2 text-right font-mono">{{ formatMoney(c.new_cost) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <template #footer>
        <div class="flex justify-end gap-2">
          <button class="btn btn-secondary" @click="backfillDialogOpen = false">{{ t('common.cancel') }}</button>
          <button
            class="btn btn-danger"
            :disabled="backfillLoading || backfillSelected.length === 0"
            @click="applyBackfill"
          >
            {{ t('admin.profit.backfill.confirm') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Tooltip,
  Legend
} from 'chart.js'
import { Line } from 'vue-chartjs'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Toggle from '@/components/common/Toggle.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { adminAPI } from '@/api/admin'
import type {
  ProfitAccountRow,
  ProfitAmounts,
  ProfitCostBackfillPreview,
  ProfitCostStatus,
  ProfitReport
} from '@/api/admin/profit'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend)

type ProfitTab = 'group' | 'account' | 'model' | 'user'

interface TableRow {
  key: string
  name: string
  amounts: ProfitAmounts
  subscription: boolean
  account?: ProfitAccountRow
}

const { t } = useI18n()
const appStore = useAppStore()

const formatLocalDate = (date: Date): string =>
  `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`

const today = new Date()
const startDate = ref(formatLocalDate(new Date(today.getTime() - 6 * 24 * 60 * 60 * 1000)))
const endDate = ref(formatLocalDate(today))
const excludeAdmin = ref(true)
const loading = ref(false)
const report = ref<ProfitReport | null>(null)
const tabs: ProfitTab[] = ['group', 'account', 'model', 'user']
const activeTab = ref<ProfitTab>('group')

const rateDrafts = reactive<Record<number, string>>({})
const savingRateId = ref<number | null>(null)

const backfillStart = ref(formatLocalDate(new Date(today.getFullYear(), today.getMonth(), 1)))
const backfillDialogOpen = ref(false)
const backfillLoading = ref(false)
const backfillPreview = ref<ProfitCostBackfillPreview | null>(null)
const backfillSelected = ref<number[]>([])

let loadSeq = 0

async function loadReport() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const data = await adminAPI.profit.getReport({
      start_date: startDate.value,
      end_date: endDate.value,
      exclude_admin: excludeAdmin.value
    })
    if (seq !== loadSeq) return
    report.value = data
    for (const row of data.by_account) {
      rateDrafts[row.account_id] = String(row.cost_rate)
    }
  } catch (err) {
    if (seq === loadSeq) appStore.showError(extractApiErrorMessage(err, t('admin.profit.loadFailed')))
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

watch(excludeAdmin, loadReport)
onMounted(loadReport)

const tableRows = computed<TableRow[]>(() => {
  const data = report.value
  if (!data) return []
  switch (activeTab.value) {
    case 'group':
      return data.by_group.map((r) => ({
        key: `g${r.group_id}`,
        name: r.group_name || `#${r.group_id}`,
        amounts: r,
        subscription: false
      }))
    case 'model':
      return data.by_model.map((r) => ({ key: `m${r.model}`, name: r.model, amounts: r, subscription: false }))
    case 'user':
      return data.by_user.map((r) => ({
        key: `u${r.user_id}`,
        name: r.email || `#${r.user_id}`,
        amounts: r,
        subscription: false
      }))
    default:
      return data.by_account.map((r) => ({
        key: `a${r.account_id}`,
        name: r.account_name,
        amounts: r,
        subscription: r.cost_status === 'subscription',
        account: r
      }))
  }
})

function parseRate(raw: string | undefined): number | null {
  if (raw === undefined || String(raw).trim() === '') return null
  const value = Number(raw)
  return Number.isFinite(value) && value >= 0 ? value : null
}

function isRateDirty(row: ProfitAccountRow): boolean {
  const value = parseRate(rateDrafts[row.account_id])
  return value === null || Math.abs(value - row.cost_rate) > 1e-9
}

async function saveRate(row: ProfitAccountRow) {
  const value = parseRate(rateDrafts[row.account_id])
  if (value === null) {
    appStore.showError(t('admin.profit.rateInvalid'))
    return
  }
  savingRateId.value = row.account_id
  try {
    await adminAPI.accounts.update(row.account_id, { rate_multiplier: value })
    appStore.showSuccess(t('admin.profit.rateSaved'))
    await loadReport()
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    savingRateId.value = null
  }
}

async function openBackfillPreview() {
  backfillLoading.value = true
  try {
    const preview = await adminAPI.profit.previewCostBackfill(backfillStart.value)
    backfillPreview.value = preview
    backfillSelected.value = preview.candidates.map((c) => c.account_id)
    backfillDialogOpen.value = true
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('admin.profit.backfill.failed')))
  } finally {
    backfillLoading.value = false
  }
}

async function applyBackfill() {
  backfillLoading.value = true
  try {
    const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone
    const state = await adminAPI.profit.applyCostBackfill(backfillStart.value, backfillSelected.value, timezone)
    appStore.showSuccess(t('admin.profit.backfill.success', { rows: formatNumber(state.rows) }))
    backfillDialogOpen.value = false
    await loadReport()
  } catch (err) {
    appStore.showError(extractApiErrorMessage(err, t('admin.profit.backfill.failed')))
  } finally {
    backfillLoading.value = false
  }
}

const isDarkMode = computed(() => document.documentElement.classList.contains('dark'))

const trendChartData = computed(() => {
  const daily = report.value?.daily
  if (!daily?.length) return null
  return {
    labels: daily.map((d) => d.date),
    datasets: [
      { label: t('admin.profit.cards.revenue'), data: daily.map((d) => d.revenue), borderColor: '#3b82f6', backgroundColor: '#3b82f6', tension: 0.3 },
      { label: t('admin.profit.cards.cost'), data: daily.map((d) => d.cost), borderColor: '#f97316', backgroundColor: '#f97316', tension: 0.3 },
      { label: t('admin.profit.cards.profit'), data: daily.map((d) => d.profit), borderColor: '#10b981', backgroundColor: '#10b981', tension: 0.3 }
    ]
  }
})

const lineOptions = computed(() => {
  const text = isDarkMode.value ? '#e5e7eb' : '#374151'
  const grid = isDarkMode.value ? '#374151' : '#e5e7eb'
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { intersect: false, mode: 'index' as const },
    plugins: {
      legend: { position: 'top' as const, labels: { color: text, usePointStyle: true, pointStyle: 'circle', font: { size: 11 } } },
      tooltip: { callbacks: { label: (ctx: any) => `${ctx.dataset.label}: ${formatMoney(Number(ctx.raw))}` } }
    },
    scales: {
      x: { grid: { color: grid }, ticks: { color: text, font: { size: 10 } } },
      y: { grid: { color: grid }, ticks: { color: text, font: { size: 10 }, callback: (v: string | number) => formatMoney(Number(v)) } }
    }
  }
})

function formatAmount(value: number): string {
  return Math.abs(value).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

function formatMoney(value: number | null | undefined): string {
  const v = Number(value) || 0
  return `${v < 0 ? '-' : ''}$${formatAmount(v)}`
}

function formatNumber(value: number | null | undefined): string {
  return (Number(value) || 0).toLocaleString()
}

function formatPercent(value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return '—'
  return `${(value * 100).toFixed(1)}%`
}

function formatDateTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function profitClass(value: number): string {
  if (value < 0) return 'text-red-600 dark:text-red-400'
  if (value > 0) return 'text-emerald-600 dark:text-emerald-400'
  return 'text-gray-500 dark:text-gray-400'
}

function statusClass(status: ProfitCostStatus): string {
  switch (status) {
    case 'configured':
      return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'unconfigured':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default:
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
  }
}
</script>
