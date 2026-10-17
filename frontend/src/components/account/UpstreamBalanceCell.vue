<template>
  <div class="flex h-6 min-w-[7rem] items-center gap-1" data-testid="upstream-balance">
    <HelpTooltip class="-ml-1" width-class="w-max max-w-[calc(100vw-2rem)]">
      <template #trigger>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 whitespace-nowrap border-b border-dotted border-gray-300 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:border-dark-600"
          :class="state?.snapshot.error ? (data ? 'font-mono text-amber-600 dark:text-amber-400' : 'text-red-600 dark:text-red-400') : data ? (data.remaining != null && data.remaining <= 0 ? 'font-mono text-red-600 dark:text-red-400' : 'font-mono text-gray-800 dark:text-gray-200') : 'text-gray-400 dark:text-gray-500'"
          :title="details"
          :aria-label="`${t('upstreamBalance.details')}: ${details}`"
          @click.stop="open"
        >
          <template v-if="data">
            <span v-if="data.scope !== 'account'" class="font-sans text-[10px] text-gray-500 dark:text-gray-400">{{ t(`upstreamBalance.${data.scope}`) }}</span>
            <span class="tabular-nums">{{ amount }}</span>
            <Icon v-if="state?.snapshot.error" name="clock" size="xs" aria-hidden="true" />
            <Icon v-else-if="state && state.owner_account_id !== state.account_id" name="link" size="xs" aria-hidden="true" />
          </template>
          <span v-else>{{ t(state?.mode === 'disabled' ? 'upstreamBalance.disabledShort' : state?.snapshot.error ? 'upstreamBalance.failedShort' : 'upstreamBalance.pendingShort') }}</span>
        </button>
      </template>
      <div class="space-y-1"><p v-for="(line, index) in details.split('\n')" :key="index">{{ line }}</p></div>
    </HelpTooltip>
    <UpstreamRefreshButton
      v-if="state?.mode !== 'disabled'"
      :busy="busy" :disabled="saving" :label="t(busy ? 'upstreamBalance.refreshing' : 'upstreamBalance.refresh')"
      @click.stop="emit('refresh')"
    />
    <button
      type="button"
      class="inline-flex h-6 w-6 flex-shrink-0 items-center justify-center rounded text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-gray-500 dark:hover:bg-dark-700 dark:hover:text-gray-300"
      :title="t('upstreamBalance.configure')" :aria-label="t('upstreamBalance.configure')" @click.stop="open"
    ><Icon name="cog" size="xs" aria-hidden="true" /></button>
    <BaseDialog :show="show" :title="`${t('upstreamBalance.configure')} · ${name}`" :close-on-escape="!saving" :show-close-button="!saving" @close="close">
      <form :id="`upstream-balance-form-${accountId}`" class="space-y-4" @submit.prevent="save">
        <div class="space-y-1 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
          <p v-if="data" class="text-gray-900 dark:text-gray-100">{{ t(`upstreamBalance.${data.scope}`) }} <span class="tabular-nums">{{ amount }}</span></p>
          <p v-if="state && state.owner_account_id !== state.account_id" class="text-gray-500 dark:text-dark-400">{{ t('upstreamBalance.shared', { id: state.owner_account_id }) }}</p>
          <p v-if="state?.snapshot.error" class="text-amber-600 dark:text-amber-400">{{ data ? t('upstreamBalance.stale') + ' · ' : '' }}{{ state.snapshot.error }}</p>
          <p v-if="state?.snapshot.fetched_at" class="text-xs text-gray-500 dark:text-dark-400">{{ updatedAt }}</p>
          <p v-if="data?.scope === 'token'" class="text-xs text-gray-500 dark:text-dark-400">{{ t('upstreamBalance.unknownWallet') }}</p>
        </div>
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
import Icon from '@/components/icons/Icon.vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'
import UpstreamRefreshButton from './UpstreamRefreshButton.vue'
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
const updatedAt = computed(() => stateTime(props.state?.snapshot.fetched_at))
function stateTime(timestamp?: number) {
  return timestamp ? t('upstreamBalance.updated', { time: new Date(timestamp * 1000).toLocaleString() }) : ''
}
const details = computed(() => {
  const lines: string[] = []
  if (data.value) lines.push(`${t(`upstreamBalance.${data.value.scope}`)} ${amount.value}`)
  else lines.push(t(props.state?.mode === 'disabled' ? 'upstreamBalance.disabled' : props.state?.snapshot.error ? 'upstreamBalance.failed' : 'upstreamBalance.pending'))
  if (data.value?.scope === 'token') lines.push(t('upstreamBalance.unknownWallet'))
  if (props.state && props.state.owner_account_id !== props.state.account_id) lines.push(t('upstreamBalance.shared', { id: props.state.owner_account_id }))
  if (props.state?.snapshot.error) lines.push(`${data.value ? t('upstreamBalance.stale') + ' · ' : ''}${props.state.snapshot.error}`)
  if (updatedAt.value) lines.push(updatedAt.value)
  return lines.join('\n')
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
