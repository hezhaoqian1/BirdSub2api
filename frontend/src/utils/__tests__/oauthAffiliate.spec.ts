import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  captureAffiliateInviteLink,
  clearAffiliateReferralCode,
  clearOAuthAffiliateCode,
  loadAffiliateReferralCode,
  loadLockedAffiliateReferralCode,
  loadOAuthAffiliateCode,
  resolveAffiliateReferralCode,
  storeAffiliateReferralCode,
  storeOAuthAffiliateCode
} from '@/utils/oauthAffiliate'

describe('oauthAffiliate', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.useRealTimers()
  })

  it('persists affiliate referral code across pages', () => {
    expect(resolveAffiliateReferralCode(' 5579J7CFG9PF ')).toBe('5579J7CFG9PF')
    expect(loadAffiliateReferralCode()).toBe('5579J7CFG9PF')
    expect(resolveAffiliateReferralCode()).toBe('5579J7CFG9PF')
  })

  it('expires stale affiliate referral code', () => {
    const now = Date.UTC(2026, 0, 1)
    storeAffiliateReferralCode('AFF123', now)

    expect(loadAffiliateReferralCode(now + 30 * 24 * 60 * 60 * 1000 - 1)).toBe('AFF123')
    expect(loadAffiliateReferralCode(now + 30 * 24 * 60 * 60 * 1000 + 1)).toBe('')
    expect(localStorage.getItem('affiliate_referral_code')).toBeNull()
  })

  it('keeps oauth transient code separate from persistent referral code', () => {
    storeAffiliateReferralCode('PERSISTED')
    storeOAuthAffiliateCode('OAUTH')

    expect(loadAffiliateReferralCode()).toBe('PERSISTED')
    expect(loadOAuthAffiliateCode()).toBe('OAUTH')

    clearOAuthAffiliateCode()
    expect(loadOAuthAffiliateCode()).toBe('')
    expect(loadAffiliateReferralCode()).toBe('PERSISTED')

    clearAffiliateReferralCode()
    expect(loadAffiliateReferralCode()).toBe('')
  })

  it('locks the code for this browser session when arriving via an invite link', () => {
    expect(captureAffiliateInviteLink(' vip2026 ')).toBe('VIP2026')
    expect(loadAffiliateReferralCode()).toBe('VIP2026')
    expect(loadLockedAffiliateReferralCode()).toBe('VIP2026')

    // A later link replaces the lock (last touch wins).
    expect(captureAffiliateInviteLink('KOL888')).toBe('KOL888')
    expect(loadLockedAffiliateReferralCode()).toBe('KOL888')

    // Closing the browser drops sessionStorage: the code is still prefilled but no longer locked.
    sessionStorage.clear()
    expect(loadLockedAffiliateReferralCode()).toBe('')
    expect(loadAffiliateReferralCode()).toBe('KOL888')
  })

  it('ignores malformed invite link codes', () => {
    expect(captureAffiliateInviteLink('a b')).toBe('')
    expect(captureAffiliateInviteLink('<script>')).toBe('')
    expect(captureAffiliateInviteLink(['AB'])).toBe('')
    expect(loadLockedAffiliateReferralCode()).toBe('')
    expect(loadAffiliateReferralCode()).toBe('')
  })

  it('drops the lock together with the referral code', () => {
    captureAffiliateInviteLink('VIP2026')
    clearAffiliateReferralCode()
    expect(loadLockedAffiliateReferralCode()).toBe('')
  })
})
