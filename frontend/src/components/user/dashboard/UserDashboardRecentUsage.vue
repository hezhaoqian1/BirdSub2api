<template>
  <div class="recent-usage card">
    <div class="flex items-end justify-between border-b px-6 py-4 dark:border-dark-700">
      <div>
        <p class="recent-usage-kicker">BirdAPI / Request log</p>
        <h2 class="recent-usage-title text-gray-900 dark:text-white">{{ t('dashboard.recentUsage') }}</h2>
      </div>
      <span class="badge badge-gray">{{ t('dashboard.last7Days') }}</span>
    </div>
    <div class="p-6">
      <div v-if="loading" class="flex items-center justify-center py-12">
        <LoadingSpinner size="lg" />
      </div>
      <div v-else-if="data.length === 0" class="py-8">
        <EmptyState :title="t('dashboard.noUsageRecords')" :description="t('dashboard.startUsingApi')" />
      </div>
      <div v-else class="divide-y divide-black/10 dark:divide-white/10">
        <div v-for="log in data" :key="log.id" class="recent-usage-row flex items-center justify-between px-2 py-4 transition-colors">
          <div class="flex items-center gap-4">
            <div class="recent-usage-icon flex h-10 w-10 items-center justify-center">
              <Icon name="beaker" size="md" class="text-primary-600 dark:text-primary-400" />
            </div>
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-white">{{ log.model }}</p>
              <p class="text-xs text-gray-500 dark:text-dark-400">{{ formatDateTime(log.created_at) }}</p>
            </div>
          </div>
          <div class="text-right">
            <p class="text-sm font-semibold">
              <span class="text-green-600 dark:text-green-400" :title="t('dashboard.actual')">${{ formatCost(log.actual_cost) }}</span>
              <span class="font-normal text-gray-400 dark:text-gray-500" :title="t('dashboard.standard')"> / ${{ formatCost(log.total_cost) }}</span>
            </p>
            <p class="text-xs text-gray-500 dark:text-dark-400">{{ (log.input_tokens + log.output_tokens).toLocaleString() }} tokens</p>
          </div>
        </div>

        <router-link to="/usage" class="flex items-center justify-center gap-2 py-3 text-sm font-medium text-primary-600 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300">
          {{ t('dashboard.viewAllUsage') }}
          <Icon name="arrowRight" size="sm" />
        </router-link>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import type { UsageLog } from '@/types'

defineProps<{
  data: UsageLog[]
  loading: boolean
}>()
const { t } = useI18n()
const formatCost = (c: number) => c.toFixed(4)
</script>

<style scoped>
.recent-usage {
  overflow: hidden;
  border-top: 3px solid var(--bird-blue);
}

.recent-usage > div:first-child {
  border-color: var(--bird-line);
}

.recent-usage-kicker {
  color: var(--bird-muted);
  font-family: var(--bird-font-mono);
  font-size: 0.62rem;
  font-weight: 700;
  text-transform: uppercase;
}

.recent-usage-title {
  margin-top: 0.3rem;
  font-family: var(--bird-font-display);
  font-size: 1.35rem;
  font-weight: 650;
}

.recent-usage-row:hover {
  background: rgba(23, 23, 20, 0.04);
}

.recent-usage-icon {
  border: 1px solid var(--bird-line);
  border-radius: 2px;
  background: var(--bird-paper-deep);
}

.dark .recent-usage-row:hover,
.dark .recent-usage-icon {
  background: rgba(255, 255, 255, 0.055);
}
</style>
