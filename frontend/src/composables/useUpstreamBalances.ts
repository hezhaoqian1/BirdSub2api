import { onMounted, onUnmounted, ref, watch, type Ref } from 'vue'
import { queryUpstreamBalances, type UpstreamBalanceState } from '@/api/admin/upstreamBalances'

interface BalanceAccount { id: number; type: string; credentials?: Record<string, unknown> }

export function useUpstreamBalances(accounts: Ref<BalanceAccount[]>) {
  const states = ref<Record<number, UpstreamBalanceState>>({})
  const busy = ref<Record<number, boolean>>({})
  const error = ref('')
  let controller = new AbortController()
  let timer: ReturnType<typeof setInterval> | undefined
  let generation = 0
  let disposed = false
  const eligibleIDs = () => accounts.value.filter(a => a.type === 'apikey' && a.credentials?.base_url).map(a => a.id)

  function merge(items: UpstreamBalanceState[]) {
    for (const item of items) states.value[item.account_id] = item
  }

  async function refresh(ids = eligibleIDs(), force = true) {
    if (disposed || document.visibilityState === 'hidden') return
    const walletIDs = new Set(ids.map(id => states.value[id]?.owner_account_id || id))
    const relatedIDs = eligibleIDs().filter(id => walletIDs.has(states.value[id]?.owner_account_id || id))
    const pending = [...new Set([...ids, ...relatedIDs])].filter(id => !busy.value[id] && states.value[id]?.mode !== 'disabled')
    const currentGeneration = generation
    const signal = controller.signal
    pending.forEach(id => { busy.value[id] = true })
    try {
      // Small batches keep one slow relay from blocking every result on the page.
      for (let offset = 0; offset < pending.length; offset += 10) {
        if (signal.aborted) return
        const batch = pending.slice(offset, offset + 10)
        const result = await queryUpstreamBalances(batch, true, force, signal)
        if (currentGeneration !== generation || disposed) return
        merge(result)
        batch.forEach(id => { busy.value[id] = false })
      }
      error.value = ''
    } catch {
      if (!signal.aborted) error.value = 'upstreamBalance.requestFailed'
    } finally {
      if (currentGeneration === generation) pending.forEach(id => { busy.value[id] = false })
    }
  }

  async function load() {
    if (disposed || document.visibilityState === 'hidden') return
    controller.abort()
    controller = new AbortController()
    const currentGeneration = ++generation
    const signal = controller.signal
    busy.value = {}
    const ids = eligibleIDs()
    states.value = Object.fromEntries(Object.entries(states.value).filter(([id]) => ids.includes(Number(id))))
    if (!ids.length || disposed) return
    try {
      for (let i = 0; i < ids.length; i += 100) {
        const result = await queryUpstreamBalances(ids.slice(i, i + 100), false, false, signal)
        if (currentGeneration !== generation || disposed) return
        merge(result)
      }
      error.value = ''
      const now = Date.now() / 1000
      await refresh(ids.filter(id => now - (states.value[id]?.snapshot.attempt_at || 0) >= 300), false)
    } catch {
      if (!signal.aborted) error.value = 'upstreamBalance.requestFailed'
    }
  }

  async function configured(state: UpstreamBalanceState) {
    if (disposed) return
    // Invalidate requests started before configuration changed, including siblings.
    controller.abort()
    generation++
    controller = new AbortController()
    busy.value = {}
    states.value[state.account_id] = state
    await refresh([state.account_id], true)
    await load()
  }

  // Rotating credentials must immediately hide the old wallet and cancel its query.
  let identities = new Map<number, string>()
  watch(() => accounts.value.map(a => [a.id, JSON.stringify([a.type, a.credentials?.base_url, a.credentials?.api_key])] as const), entries => {
    for (const [id, identity] of entries) {
      if (identities.get(id) !== identity) delete states.value[id]
    }
    identities = new Map(entries)
    void load()
  }, { immediate: true })
  function visibilityChanged() {
    if (document.visibilityState === 'hidden') {
      controller.abort()
      generation++
      busy.value = {}
    } else if (!disposed) {
      void load()
    }
  }
  onMounted(() => {
    document.addEventListener('visibilitychange', visibilityChanged)
    timer = setInterval(() => {
      if (document.visibilityState === 'visible' && !Object.values(busy.value).some(Boolean)) void load()
    }, 300000)
  })
  onUnmounted(() => {
    disposed = true
    controller.abort()
    document.removeEventListener('visibilitychange', visibilityChanged)
    if (timer) clearInterval(timer)
  })
  return { balanceStates: states, balanceBusy: busy, balanceError: error, refreshBalances: refresh, balanceConfigured: configured }
}
