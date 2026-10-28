<template>
  <BaseDialog :show="show" :title="t('securityAlerts.userWatch.title')" width="narrow" @close="$emit('close')">
    <form v-if="user" id="balance-watch-form" class="space-y-5" @submit.prevent="handleSubmit">
      <div class="flex items-center gap-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <div class="flex h-10 w-10 items-center justify-center rounded-full bg-primary-100">
          <span class="text-lg font-medium text-primary-700">{{ user.email.charAt(0).toUpperCase() }}</span>
        </div>
        <div class="flex-1">
          <p class="font-medium text-gray-900 dark:text-gray-100">{{ user.email }}</p>
          <p class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('securityAlerts.userWatch.currentBalance') }}: ${{ user.balance.toFixed(2) }}
          </p>
        </div>
      </div>

      <p
        v-if="!alertsEnabled || !webhookConfigured"
        class="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-700 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-300"
        data-testid="balance-watch-channel-warning"
      >
        {{ !alertsEnabled ? t('securityAlerts.userWatch.alertsDisabled') : t('securityAlerts.userWatch.webhookMissing') }}
      </p>

      <div>
        <label class="input-label" for="balance-watch-threshold">{{ t('securityAlerts.userWatch.thresholdLabel') }}</label>
        <div class="relative">
          <div class="absolute left-3 top-1/2 -translate-y-1/2 font-medium text-gray-500">$</div>
          <input
            id="balance-watch-threshold"
            v-model="form.threshold"
            type="number"
            step="0.01"
            min="0.01"
            class="input pl-8"
            :placeholder="t('securityAlerts.userWatch.thresholdPlaceholder')"
            data-testid="balance-watch-threshold"
          />
        </div>
        <p class="input-hint">{{ t('securityAlerts.userWatch.thresholdHint') }}</p>
      </div>

      <div>
        <label class="input-label" for="balance-watch-note">{{ t('securityAlerts.userWatch.noteLabel') }}</label>
        <input
          id="balance-watch-note"
          v-model="form.note"
          type="text"
          maxlength="60"
          class="input"
          :placeholder="t('securityAlerts.userWatch.notePlaceholder')"
          data-testid="balance-watch-note"
        />
        <p class="input-hint">{{ t('securityAlerts.userWatch.noteHint') }}</p>
      </div>

      <div class="rounded-xl border border-blue-200 bg-blue-50 p-4 text-sm text-gray-700 dark:border-blue-800 dark:bg-blue-950 dark:text-gray-300">
        <p class="mb-1 font-medium text-gray-900 dark:text-gray-100">{{ t('securityAlerts.userWatch.rulesTitle') }}</p>
        <ul class="list-disc space-y-1 pl-5">
          <li>{{ t('securityAlerts.userWatch.ruleCheck') }}</li>
          <li>{{ t('securityAlerts.userWatch.ruleOnce') }}</li>
          <li>{{ t('securityAlerts.userWatch.ruleRearm') }}</li>
          <li>{{ t('securityAlerts.userWatch.ruleDigest') }}</li>
        </ul>
        <p v-if="alreadyBelow" class="mt-2 font-medium text-amber-700 dark:text-amber-300" data-testid="balance-watch-already-below">
          {{ t('securityAlerts.userWatch.alreadyBelow') }}
        </p>
      </div>

      <p v-if="error" class="text-sm text-red-600 dark:text-red-400" data-testid="balance-watch-error">{{ error }}</p>
    </form>
    <template #footer>
      <div class="flex items-center justify-between gap-3">
        <div>
          <button
            v-if="watch"
            type="button"
            class="btn btn-secondary text-red-600 dark:text-red-400"
            :disabled="busy"
            data-testid="balance-watch-remove"
            @click="handleRemove"
          >
            {{ t('securityAlerts.userWatch.remove') }}
          </button>
        </div>
        <div class="flex gap-3">
          <button type="button" class="btn btn-secondary" @click="$emit('close')">{{ t('common.cancel') }}</button>
          <button
            type="submit"
            form="balance-watch-form"
            class="btn btn-primary"
            :disabled="busy"
            data-testid="balance-watch-save"
          >
            {{ busy ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch as watchEffectOn } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { removeUserWatch, setUserWatch } from '@/api/admin/securityAlerts'
import type { UserBalanceWatch } from '@/api/admin/securityAlerts'
import { extractI18nErrorMessage } from '@/utils/apiError'
import type { AdminUser } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{
  show: boolean
  user: AdminUser | null
  watch: UserBalanceWatch | null
  alertsEnabled: boolean
  webhookConfigured: boolean
}>()
const emit = defineEmits<{
  close: []
  saved: [watch: UserBalanceWatch]
  removed: [userId: number]
}>()
const { t } = useI18n()
const appStore = useAppStore()

// <input type="number"> 的 v-model 会把值转成 number，清空时是空字符串。
const form = reactive<{ threshold: number | string; note: string }>({ threshold: '', note: '' })
const busy = ref(false)
const error = ref('')

watchEffectOn(() => props.show, (open) => {
  if (!open) return
  form.threshold = props.watch ? props.watch.threshold : ''
  form.note = props.watch?.note ?? ''
  error.value = ''
})

const thresholdValue = computed(() => {
  const n = typeof form.threshold === 'number' ? form.threshold : Number(String(form.threshold).trim())
  return String(form.threshold).trim() === '' || !Number.isFinite(n) ? NaN : n
})

const alreadyBelow = computed(() =>
  !!props.user && thresholdValue.value > 0 && props.user.balance < thresholdValue.value
)

async function handleSubmit() {
  if (!props.user || busy.value) return
  const threshold = Math.round(thresholdValue.value * 100) / 100
  if (!(threshold > 0)) {
    error.value = t('securityAlerts.thresholdInvalid')
    return
  }
  busy.value = true
  error.value = ''
  try {
    const saved = await setUserWatch(props.user.id, { threshold, note: form.note.trim() })
    appStore.showSuccess(t('securityAlerts.userWatch.saved'))
    emit('saved', saved)
    emit('close')
  } catch (err) {
    error.value = extractI18nErrorMessage(err, t, 'securityAlerts.userWatch.errors', t('common.error'))
  } finally {
    busy.value = false
  }
}

async function handleRemove() {
  if (!props.user || busy.value) return
  busy.value = true
  error.value = ''
  try {
    await removeUserWatch(props.user.id)
    appStore.showSuccess(t('securityAlerts.userWatch.removed'))
    emit('removed', props.user.id)
    emit('close')
  } catch (err) {
    error.value = extractI18nErrorMessage(err, t, 'securityAlerts.userWatch.errors', t('common.error'))
  } finally {
    busy.value = false
  }
}
</script>
