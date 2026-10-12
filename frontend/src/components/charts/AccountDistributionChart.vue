<template>
  <div class="p-4">
    <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
      <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('admin.dashboard.accountDistribution') }}
      </h3>
      <div
        class="inline-flex rounded-lg border border-gray-200 bg-gray-50 p-0.5 dark:border-dark-700 dark:bg-dark-800"
      >
        <button
          type="button"
          class="rounded-md px-2.5 py-1 text-xs font-medium transition-colors"
          :class="metric === 'account_cost'
            ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
            : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
          :aria-pressed="metric === 'account_cost'"
          @click="emit('update:metric', 'account_cost')"
        >
          {{ t('admin.dashboard.metricAccountCost') }}
        </button>
        <button
          type="button"
          class="rounded-md px-2.5 py-1 text-xs font-medium transition-colors"
          :class="metric === 'tokens'
            ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
            : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
          :aria-pressed="metric === 'tokens'"
          @click="emit('update:metric', 'tokens')"
        >
          {{ t('admin.dashboard.metricTokens') }}
        </button>
        <button
          type="button"
          class="rounded-md px-2.5 py-1 text-xs font-medium transition-colors"
          :class="metric === 'actual_cost'
            ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white'
            : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'"
          :aria-pressed="metric === 'actual_cost'"
          @click="emit('update:metric', 'actual_cost')"
        >
          {{ t('admin.dashboard.metricActualCost') }}
        </button>
      </div>
    </div>
    <div v-if="loading" class="flex h-48 items-center justify-center">
      <LoadingSpinner />
    </div>
    <div v-else-if="error" role="alert" class="flex h-48 flex-col items-center justify-center gap-2 text-sm text-gray-500">
      <span>{{ t('admin.dashboard.accountStatsFailed') }}</span>
      <button type="button" class="text-primary-600 hover:underline" @click="emit('retry')">{{ t('admin.dashboard.retryAccountStats') }}</button>
    </div>
    <div v-else-if="displayAccountStats.length > 0 && chartData" class="flex flex-col items-center gap-3 sm:flex-row">
      <div v-if="showChart" class="h-28 w-28 shrink-0">
        <Doughnut :data="chartData" :options="doughnutOptions" />
      </div>
      <div class="max-h-48 w-full min-w-0 flex-1 overflow-auto">
        <table class="w-full whitespace-nowrap text-xs [&_td]:px-1.5 [&_th]:px-1.5">
          <thead>
            <tr class="text-gray-500 dark:text-gray-400">
              <th class="pb-2 text-left">{{ t('admin.dashboard.upstreamAccount') }}</th>
              <th class="pb-2 text-right">{{ t('admin.dashboard.requests') }}</th>
              <th class="pb-2 text-right">{{ t('admin.dashboard.tokens') }}</th>
              <th class="pb-2 text-right">{{ t('admin.dashboard.userCharge') }}</th>
              <th class="pb-2 text-right">{{ t('admin.dashboard.upstreamCost') }}</th>
              <th class="pb-2 text-right">{{ t('admin.dashboard.standard') }}</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="account in displayAccountStats" :key="account.account_id">
              <tr
                class="border-t border-gray-100 transition-colors dark:border-dark-700"
              >
                <td
                  class="min-w-[120px] max-w-[160px] whitespace-normal break-words py-1.5 font-medium text-gray-900 dark:text-white"
                  :title="accountLabel(account)"
                >
                  <span>
                    {{ accountLabel(account) }}
                  </span>
                </td>
                <td class="py-1.5 text-right text-gray-600 dark:text-gray-400">
                  {{ formatNumber(account.requests) }}
                </td>
                <td class="py-1.5 text-right text-gray-600 dark:text-gray-400">
                  {{ formatTokens(account.total_tokens) }}
                </td>
                <td class="py-1.5 text-right text-green-600 dark:text-green-400">
                  ${{ formatCost(account.actual_cost) }}
                </td>
                <td class="py-1.5 text-right text-orange-500 dark:text-orange-400">
                  ${{ formatCost(account.account_cost) }}
                </td>
                <td class="py-1.5 text-right text-gray-400 dark:text-gray-500">
                  ${{ formatCost(account.cost) }}
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </div>
    <div
      v-else
      class="flex h-48 items-center justify-center text-sm text-gray-500 dark:text-gray-400"
    >
      {{ t('admin.dashboard.noDataAvailable') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, ArcElement, Tooltip, Legend } from 'chart.js'
import { Doughnut } from 'vue-chartjs'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import type { AccountStat } from '@/types'

ChartJS.register(ArcElement, Tooltip, Legend)

const { t } = useI18n()

type DistributionMetric = 'tokens' | 'actual_cost' | 'account_cost'

const props = withDefaults(defineProps<{
  accountStats: AccountStat[]
  loading?: boolean
  metric?: DistributionMetric
  error?: boolean
  showChart?: boolean
}>(), {
  loading: false,
  metric: 'account_cost',
  error: false,
  showChart: true,
})

const emit = defineEmits<{
  'update:metric': [value: DistributionMetric]
  retry: []
}>()

const accountLabel = (account: AccountStat) => account.account_name
  ? `${account.account_name} (#${account.account_id})`
  : account.account_id > 0 ? `#${account.account_id}` : t('admin.dashboard.unknownAccount')

const chartColors = [
  '#3b82f6',
  '#10b981',
  '#f59e0b',
  '#ef4444',
  '#8b5cf6',
  '#ec4899',
  '#14b8a6',
  '#f97316',
  '#6366f1',
  '#84cc16'
]

const displayAccountStats = computed(() => {
  if (!props.accountStats?.length) return []

  const metricKey = props.metric === 'tokens' ? 'total_tokens' : props.metric
  return [...props.accountStats].sort((a, b) => toFiniteNumber(b[metricKey]) - toFiniteNumber(a[metricKey]))
})

const chartData = computed(() => {
  if (!props.accountStats?.length) return null

  return {
    labels: displayAccountStats.value.map((g) => accountLabel(g)),
    datasets: [
      {
        data: displayAccountStats.value.map((g) => toFiniteNumber(props.metric === 'tokens' ? g.total_tokens : g[props.metric])),
        backgroundColor: displayAccountStats.value.map((_, index) => chartColors[index % chartColors.length]),
        borderWidth: 0
      }
    ]
  }
})

const doughnutOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: {
      display: false
    },
    tooltip: {
      callbacks: {
        label: (context: any) => {
          const value = context.raw as number
          const total = context.dataset.data.reduce((a: number, b: number) => a + b, 0)
          const percentage = total > 0 ? ((value / total) * 100).toFixed(1) : '0.0'
          const formattedValue = props.metric !== 'tokens'
            ? `$${formatCost(value)}`
            : formatTokens(value)
          return `${context.label}: ${formattedValue} (${percentage}%)`
        }
      }
    }
  }
}))

const formatTokens = (value: number): string => {
  if (value >= 1_000_000_000) {
    return `${(value / 1_000_000_000).toFixed(2)}B`
  } else if (value >= 1_000_000) {
    return `${(value / 1_000_000).toFixed(2)}M`
  } else if (value >= 1_000) {
    return `${(value / 1_000).toFixed(2)}K`
  }
  return value.toLocaleString()
}

const formatNumber = (value: number): string => {
  return toFiniteNumber(value).toLocaleString()
}

const toFiniteNumber = (value: unknown): number => {
  const numberValue = Number(value)
  return Number.isFinite(numberValue) ? numberValue : 0
}

const formatCost = (value: number | null | undefined): string => {
  const safeValue = toFiniteNumber(value)
  if (safeValue >= 1000) {
    return (safeValue / 1000).toFixed(2) + 'K'
  } else if (safeValue >= 1) {
    return safeValue.toFixed(2)
  } else if (safeValue >= 0.01) {
    return safeValue.toFixed(3)
  }
  return safeValue.toFixed(4)
}
</script>
