/**
 * Subscription Store
 * Global state management for user subscriptions with caching and deduplication
 */

import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import subscriptionsAPI from '@/api/subscriptions'
import type { UserSubscription } from '@/types'

// Cache TTL: 60 seconds
const CACHE_TTL_MS = 60_000

// Request generation counter to invalidate stale in-flight responses
let requestGeneration = 0

export const useSubscriptionStore = defineStore('subscriptions', () => {
  // State
  const activeSubscriptions = ref<UserSubscription[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const lastFetchedAt = ref<number | null>(null)

  // In-flight request deduplication
  let activePromise: Promise<UserSubscription[]> | null = null

  // Auto-refresh interval
  let pollerInterval: ReturnType<typeof setInterval> | null = null
  let activeRequestController: AbortController | null = null

  // Computed
  const hasActiveSubscriptions = computed(() => activeSubscriptions.value.length > 0)

  /**
   * Fetch active subscriptions with caching and deduplication
   * @param force - Force refresh even if cache is valid
   */
  async function fetchActiveSubscriptions(force = false): Promise<UserSubscription[]> {
    const now = Date.now()

    // Return cached data if valid
    if (
      !force &&
      loaded.value &&
      lastFetchedAt.value &&
      now - lastFetchedAt.value < CACHE_TTL_MS
    ) {
      return activeSubscriptions.value
    }

    // Return in-flight request if exists (deduplication)
    if (activePromise && !force) {
      return activePromise
    }

    const currentGeneration = ++requestGeneration

    // Start new request
    loading.value = true
    const requestController = new AbortController()
    activeRequestController = requestController
    const requestPromise = subscriptionsAPI
      .getActiveSubscriptions(requestController.signal)
      .then((data) => {
        if (currentGeneration === requestGeneration) {
          activeSubscriptions.value = data
          loaded.value = true
          lastFetchedAt.value = Date.now()
        }
        return data
      })
      .catch((error) => {
        // Logout or another session reset invalidates this request. The request
        // may still reject after the auth state is gone; do not surface it as a
        // real subscription failure to the page or console.
        if (currentGeneration !== requestGeneration) {
          return []
        }
        console.error('Failed to fetch active subscriptions:', error)
        throw error
      })
      .finally(() => {
        if (activePromise === requestPromise) {
          loading.value = false
          activePromise = null
        }
        if (activeRequestController === requestController) {
          activeRequestController = null
        }
      })

    activePromise = requestPromise

    return activePromise
  }

  /**
   * Start auto-refresh polling 
   */
  function startPolling() {
    if (pollerInterval) return

    pollerInterval = setInterval(() => {
      fetchActiveSubscriptions(true).catch((error) => {
        console.error('Subscription polling failed:', error)
      })
    }, 5 * 60 * 1000)
  }

  /**
   * Stop auto-refresh polling
   */
  function stopPolling() {
    if (pollerInterval) {
      clearInterval(pollerInterval)
      pollerInterval = null
    }
  }

  /**
   * Clear all subscription data and stop polling
   */
  function clear() {
    requestGeneration++
    activeRequestController?.abort()
    activeRequestController = null
    activePromise = null
    loading.value = false
    activeSubscriptions.value = []
    loaded.value = false
    lastFetchedAt.value = null
    stopPolling()
  }

  /**
   * Invalidate cache (force next fetch to reload)
   */
  function invalidateCache() {
    lastFetchedAt.value = null
  }

  return {
    // State
    activeSubscriptions,
    loading,
    hasActiveSubscriptions,

    // Actions
    fetchActiveSubscriptions,
    startPolling,
    stopPolling,
    clear,
    invalidateCache
  }
})
