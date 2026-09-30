<template>
  <div class="workbench-actions card">
    <div class="border-b px-6 py-4 dark:border-dark-700">
      <p class="workbench-kicker">BirdAPI / Next steps</p>
      <h2 class="workbench-title text-gray-900 dark:text-white">{{ t('dashboard.quickActions') }}</h2>
    </div>
    <div class="divide-y divide-black/10 px-4 dark:divide-white/10">
      <button @click="router.push('/keys')" class="workbench-action group flex w-full items-center gap-4 px-2 py-4 text-left transition-colors">
        <div class="workbench-action-icon flex h-10 w-10 flex-shrink-0 items-center justify-center">
          <Icon name="key" size="lg" class="text-primary-600 dark:text-primary-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.createApiKey') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.generateNewKey') }}</p>
        </div>
        <Icon
          name="chevronRight"
          size="md"
          class="text-gray-400 transition-colors group-hover:text-primary-500 dark:text-dark-500"
        />
      </button>

      <button @click="router.push('/usage')" class="workbench-action group flex w-full items-center gap-4 px-2 py-4 text-left transition-colors">
        <div class="workbench-action-icon flex h-10 w-10 flex-shrink-0 items-center justify-center">
          <Icon name="chart" size="lg" class="text-emerald-600 dark:text-emerald-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.viewUsage') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.checkDetailedLogs') }}</p>
        </div>
        <Icon
          name="chevronRight"
          size="md"
          class="text-gray-400 transition-colors group-hover:text-emerald-500 dark:text-dark-500"
        />
      </button>

      <button v-if="canUseBatchImage" @click="router.push('/batch-image')" class="workbench-action group flex w-full items-center gap-4 px-2 py-4 text-left transition-colors">
        <div class="workbench-action-icon flex h-10 w-10 flex-shrink-0 items-center justify-center">
          <Icon name="sparkles" size="lg" class="text-sky-600 dark:text-sky-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.batchImageAgent') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.batchImageAgentDesc') }}</p>
        </div>
        <Icon
          name="chevronRight"
          size="md"
          class="text-gray-400 transition-colors group-hover:text-sky-500 dark:text-dark-500"
        />
      </button>

      <button @click="router.push('/redeem')" class="workbench-action group flex w-full items-center gap-4 px-2 py-4 text-left transition-colors">
        <div class="workbench-action-icon flex h-10 w-10 flex-shrink-0 items-center justify-center">
          <Icon name="gift" size="lg" class="text-amber-600 dark:text-amber-400" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.redeemCode') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.addBalanceWithCode') }}</p>
        </div>
        <Icon
          name="chevronRight"
          size="md"
          class="text-gray-400 transition-colors group-hover:text-amber-500 dark:text-dark-500"
        />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useBatchImageAccess } from '@/composables/useBatchImageAccess'
const router = useRouter()
const { t } = useI18n()
const { canUseBatchImage, refreshBatchImageAccess } = useBatchImageAccess()

onMounted(() => {
  void refreshBatchImageAccess()
})
</script>

<style scoped>
.workbench-actions {
  overflow: hidden;
  border-top: 3px solid var(--bird-red);
}

.workbench-actions > div:first-child {
  border-color: var(--bird-line);
}

.workbench-kicker {
  color: var(--bird-muted);
  font-family: var(--bird-font-mono);
  font-size: 0.62rem;
  font-weight: 700;
  text-transform: uppercase;
}

.workbench-title {
  margin-top: 0.3rem;
  font-family: var(--bird-font-display);
  font-size: 1.35rem;
  font-weight: 650;
}

.workbench-action:hover {
  background: rgba(23, 23, 20, 0.045);
}

.workbench-action-icon {
  border: 1px solid var(--bird-line);
  border-radius: 2px;
  background: var(--bird-paper-deep);
}

.dark .workbench-action:hover,
.dark .workbench-action-icon {
  background: rgba(255, 255, 255, 0.055);
}
</style>
