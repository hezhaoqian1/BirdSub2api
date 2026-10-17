<template>
  <div class="mt-1 max-w-[240px] space-y-0.5 text-xs" data-testid="upstream-balance">
    <div class="flex flex-wrap items-center gap-x-2 gap-y-0.5">
      <span v-if="data" :class="data.remaining != null && data.remaining <= 0 ? 'text-red-600 dark:text-red-400' : 'text-gray-600 dark:text-gray-300'">
        {{ t(`upstreamBalance.${data.scope}`) }}
        <strong class="font-medium tabular-nums">{{ amount }}</strong>
      </span>
      <span v-else class="text-gray-400">{{ t(state?.mode === 'disabled' ? 'upstreamBalance.disabled' : state?.snapshot.error ? 'upstreamBalance.failed' : 'upstreamBalance.pending') }}</span>
      <button v-if="state?.mode !== 'disabled'" type="button" class="text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :disabled="busy || saving" @click.stop="emit('refresh')">{{ t(busy ? 'upstreamBalance.refreshing' : 'upstreamBalance.refresh') }}</button>
      <button type="button" class="text-gray-500 hover:underline dark:text-gray-400" :aria-label="t('upstreamBalance.configure')" @click.stop="open">{{ t('upstreamBalance.configureShort') }}</button>
    </div>
    <div v-if="state && state.owner_account_id !== state.account_id" class="text-primary-600 dark:text-primary-400">{{ t('upstreamBalance.shared', { id: state.owner_account_id }) }}</div>
    <div v-if="data?.scope === 'token'" class="text-amber-600 dark:text-amber-400" :title="tokenHint">{{ t('upstreamBalance.unknownWallet') }}</div>
    <div v-if="state?.snapshot.error" class="truncate text-amber-600 dark:text-amber-400" :title="state.snapshot.error">
      {{ data ? t('upstreamBalance.stale') + ' · ' : '' }}{{ state.snapshot.error }}
    </div>
    <div v-if="state?.snapshot.fetched_at" class="text-[10px] text-gray-400">{{ t('upstreamBalance.updated', { time: new Date(state.snapshot.fetched_at * 1000).toLocaleString() }) }}</div>
    <BaseDialog :show="show" :title="`${t('upstreamBalance.configure')} · ${name}`" :close-on-escape="!saving" :show-close-button="!saving" @close="close">
      <form :id="`upstream-balance-form-${accountId}`" class="space-y-4" @submit.prevent="save">
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('upstreamBalance.autoHint') }}</p>
        <label class="block text-sm">
          <span class="mb-1 block">{{ t('upstreamBalance.mode') }}</span>
          <select v-model="form.mode" class="input w-full" :disabled="saving">
            <option v-for="option in modes" :key="option.value" :value="option.value">{{ t(`upstreamBalance.${option.label}`) }}</option>
          </select>
        </label>
        <template v-if="form.mode === 'new_api'">
          <p v-if="!state?.encryption_configured" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-900/20 dark:text-amber-300">{{ t('upstreamBalance.encryptionMissing') }}</p>
          <label class="block text-sm">
            <span class="mb-1 block">{{ t('upstreamBalance.accessToken') }}</span>
            <input v-model="form.access_token" type="password" autocomplete="new-password" class="input w-full" :disabled="saving" :placeholder="t(state?.token_configured ? 'upstreamBalance.tokenSaved' : 'upstreamBalance.tokenPlaceholder')" />
            <span class="mt-1 block text-xs text-gray-500">{{ t('upstreamBalance.accessTokenHint') }}</span>
          </label>
          <label class="block text-sm">
            <span class="mb-1 block">{{ t('upstreamBalance.userId') }}</span>
            <input v-model="form.user_id" inputmode="numeric" pattern="[0-9]+" required class="input w-full" :disabled="saving" />
            <span class="mt-1 block text-xs text-gray-500">{{ t('upstreamBalance.userIdHint') }}</span>
          </label>
        </template>
        <template v-if="form.mode === 'shared'">
          <label class="block text-sm">
            <span class="mb-1 block">{{ t('upstreamBalance.owner') }}</span>
            <input v-model.number="form.owner_account_id" type="number" min="1" required class="input w-full" :disabled="saving" />
            <span class="mt-1 block text-xs text-gray-500">{{ t('upstreamBalance.ownerHint') }}</span>
          </label>
          <label class="flex items-start gap-2 text-sm"><input v-model="confirmed" type="checkbox" required :disabled="saving" />{{ t('upstreamBalance.sharedConfirm') }}</label>
        </template>
        <p v-if="data?.scope === 'token'" class="text-sm text-amber-600 dark:text-amber-400">{{ tokenHint }}</p>
        <p v-if="data?.unit === 'QUOTA'" class="text-sm text-gray-500">{{ t('upstreamBalance.quotaHint') }}</p>
        <p v-if="saveError" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ saveError }}</p>
      </form>
      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('upstreamBalance.cancel') }}</button>
        <button type="submit" :form="`upstream-balance-form-${accountId}`" class="btn btn-primary" :disabled="saving || (form.mode === 'new_api' && !state?.encryption_configured) || (form.mode === 'shared' && !confirmed)">{{ t(saving ? 'upstreamBalance.saving' : 'upstreamBalance.save') }}</button>
      </template>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { saveUpstreamBalanceConfig, type BalanceMode, type UpstreamBalanceConfig, type UpstreamBalanceState } from '@/api/admin/upstreamBalances'

const props = defineProps<{ accountId: number; name: string; state?: UpstreamBalanceState; busy?: boolean }>()
const emit = defineEmits<{ refresh: []; configured: [state: UpstreamBalanceState] }>()
const { t } = useI18n()
const show = ref(false)
const saving = ref(false)
const saveError = ref('')
const confirmed = ref(false)
const form = reactive<UpstreamBalanceConfig>({ mode: 'auto', access_token: '', user_id: '', owner_account_id: undefined })
const modes: { value: BalanceMode; label: string }[] = [
  { value: 'auto', label: 'auto' }, { value: 'sub2api', label: 'sub2api' },
  { value: 'new_api', label: 'new_api' }, { value: 'token', label: 'tokenMode' },
  { value: 'shared', label: 'sharedMode' }, { value: 'disabled', label: 'disabledMode' }
]
const data = computed(() => props.state?.snapshot.data)
const tokenHint = computed(() => t(data.value?.source === 'sub2api' ? 'upstreamBalance.sub2apiTokenHint' : 'upstreamBalance.tokenHint'))
const amount = computed(() => {
  if (data.value?.unlimited) return t('upstreamBalance.unlimited')
  if (data.value?.remaining == null) return '—'
  const unit = data.value.unit === 'QUOTA' ? t('upstreamBalance.rawQuota') : data.value.unit
  return `${data.value.remaining.toLocaleString(undefined, { maximumFractionDigits: 4 })} ${unit}`
})
function open() {
  Object.assign(form, { mode: props.state?.mode || 'auto', access_token: '', user_id: props.state?.user_id || '', owner_account_id: props.state?.mode === 'shared' ? props.state.owner_account_id : undefined })
  confirmed.value = false
  saveError.value = ''
  show.value = true
}
function close() { if (!saving.value) { show.value = false; form.access_token = '' } }
async function save() {
  if (saving.value) return
  saving.value = true
  saveError.value = ''
  try {
    const state = await saveUpstreamBalanceConfig(props.accountId, { ...form })
    form.access_token = ''
    show.value = false
    emit('configured', state)
  } catch (error: unknown) {
    // Only show the server's sanitized message, never the request/config object.
    const serverError = error as { response?: { data?: { message?: string } }; message?: string }
    saveError.value = serverError.response?.data?.message || serverError.message || t('upstreamBalance.saveFailed')
  } finally { saving.value = false }
}
</script>
