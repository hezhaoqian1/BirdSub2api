import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RouteLocation } from 'vue-router'

import { loadAffiliateReferralCode, loadLockedAffiliateReferralCode } from '@/utils/oauthAffiliate'

const authStore = vi.hoisted(() => ({
  checkAuth: vi.fn(),
  isAuthenticated: false,
  isAdmin: false,
  isSimpleMode: false,
}))

const appStore = vi.hoisted(() => ({
  siteName: 'Sub2API',
  backendModeEnabled: false,
  cachedPublicSettings: null as null | Record<string, unknown>,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authStore,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({
    customMenuItems: [],
  }),
}))

vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({
    startNavigation: vi.fn(),
    endNavigation: vi.fn(),
    isLoading: { value: false },
  }),
}))

vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => ({
    triggerPrefetch: vi.fn(),
    cancelPendingPrefetch: vi.fn(),
    resetPrefetchState: vi.fn(),
  }),
}))

async function resolveInviteRedirect(code: string) {
  const { default: router } = await import('@/router')
  const record = router.getRoutes().find((route) => route.name === 'AffiliateInvite')
  expect(record?.path).toBe('/r/:code')
  const redirect = record?.redirect as (to: RouteLocation) => unknown
  return redirect({ params: { code } } as unknown as RouteLocation)
}

describe('router affiliate short invite link', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
  })

  it('remembers and locks the code, then sends the visitor to a clean register URL', async () => {
    await expect(resolveInviteRedirect('vip2026')).resolves.toEqual({ path: '/register' })

    expect(loadAffiliateReferralCode()).toBe('VIP2026')
    expect(loadLockedAffiliateReferralCode()).toBe('VIP2026')
  })

  it('still redirects but ignores malformed codes', async () => {
    await expect(resolveInviteRedirect('<bad>')).resolves.toEqual({ path: '/register' })

    expect(loadAffiliateReferralCode()).toBe('')
    expect(loadLockedAffiliateReferralCode()).toBe('')
  })
})
