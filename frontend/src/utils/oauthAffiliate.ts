const OAUTH_AFFILIATE_CODE_KEY = 'oauth_aff_code'
const AFFILIATE_REFERRAL_CODE_KEY = 'affiliate_referral_code'
const AFFILIATE_REFERRAL_LOCK_KEY = 'affiliate_referral_locked'
const AFFILIATE_REFERRAL_TTL_MS = 30 * 24 * 60 * 60 * 1000
const AFFILIATE_CODE_PATTERN = /^[A-Za-z0-9_-]{4,32}$/

interface StoredAffiliateReferralCode {
  code: string
  expiresAt: number
}

export function normalizeOAuthAffiliateCode(value?: unknown): string {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' ? raw.trim() : ''
}

export function pickOAuthAffiliateCode(...values: unknown[]): string {
  for (const value of values) {
    const code = normalizeOAuthAffiliateCode(value)
    if (code) {
      return code
    }
  }
  return ''
}

export function storeAffiliateReferralCode(value?: unknown, now = Date.now()): void {
  if (typeof window === 'undefined') {
    return
  }
  const code = normalizeOAuthAffiliateCode(value)
  if (!code) {
    return
  }
  try {
    const payload: StoredAffiliateReferralCode = {
      code,
      expiresAt: now + AFFILIATE_REFERRAL_TTL_MS
    }
    window.localStorage.setItem(AFFILIATE_REFERRAL_CODE_KEY, JSON.stringify(payload))
  } catch {
    // 忽略浏览器存储异常。
  }
}

export function loadAffiliateReferralCode(now = Date.now()): string {
  if (typeof window === 'undefined') {
    return ''
  }
  try {
    const raw = window.localStorage.getItem(AFFILIATE_REFERRAL_CODE_KEY)
    if (!raw) {
      return ''
    }
    const parsed = JSON.parse(raw) as Partial<StoredAffiliateReferralCode>
    const code = normalizeOAuthAffiliateCode(parsed.code)
    const expiresAt = Number(parsed.expiresAt) || 0
    if (!code || expiresAt <= now) {
      clearAffiliateReferralCode()
      return ''
    }
    return code
  } catch {
    clearAffiliateReferralCode()
    return ''
  }
}

export function clearAffiliateReferralCode(): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.localStorage.removeItem(AFFILIATE_REFERRAL_CODE_KEY)
  } catch {
    // 忽略浏览器存储异常。
  }
  clearLockedAffiliateReferralCode()
}

/**
 * 记录用户是通过邀请链接（/r/:code 或 ?aff=）进来的：邀请码存 30 天，
 * 同时在 sessionStorage 里锁定，注册页的邀请码在关闭浏览器前不能修改或清空。
 * 格式不合法的码直接忽略，返回规范化（大写）后的码或空串。
 */
export function captureAffiliateInviteLink(value?: unknown, now = Date.now()): string {
  const code = normalizeOAuthAffiliateCode(value).toUpperCase()
  if (!AFFILIATE_CODE_PATTERN.test(code)) {
    return ''
  }
  storeAffiliateReferralCode(code, now)
  if (typeof window !== 'undefined') {
    try {
      window.sessionStorage.setItem(AFFILIATE_REFERRAL_LOCK_KEY, code)
    } catch {
      // 忽略浏览器存储异常。
    }
  }
  return code
}

export function loadLockedAffiliateReferralCode(): string {
  if (typeof window === 'undefined') {
    return ''
  }
  try {
    return normalizeOAuthAffiliateCode(window.sessionStorage.getItem(AFFILIATE_REFERRAL_LOCK_KEY))
  } catch {
    return ''
  }
}

export function clearLockedAffiliateReferralCode(): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.sessionStorage.removeItem(AFFILIATE_REFERRAL_LOCK_KEY)
  } catch {
    // 忽略浏览器存储异常。
  }
}

export function resolveAffiliateReferralCode(...values: unknown[]): string {
  const code = pickOAuthAffiliateCode(...values)
  if (code) {
    storeAffiliateReferralCode(code)
    return code
  }
  return loadAffiliateReferralCode()
}

export function storeOAuthAffiliateCode(value?: unknown): void {
  if (typeof window === 'undefined') {
    return
  }
  const code = normalizeOAuthAffiliateCode(value)
  try {
    if (code) {
      window.sessionStorage.setItem(OAUTH_AFFILIATE_CODE_KEY, code)
    } else {
      window.sessionStorage.removeItem(OAUTH_AFFILIATE_CODE_KEY)
    }
  } catch {
    // 忽略浏览器存储异常。
  }
}

export function loadOAuthAffiliateCode(): string {
  if (typeof window === 'undefined') {
    return ''
  }
  try {
    return normalizeOAuthAffiliateCode(window.sessionStorage.getItem(OAUTH_AFFILIATE_CODE_KEY))
  } catch {
    return ''
  }
}

export function clearOAuthAffiliateCode(): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.sessionStorage.removeItem(OAUTH_AFFILIATE_CODE_KEY)
  } catch {
    // 忽略浏览器存储异常。
  }
}

export function clearAllAffiliateReferralCodes(): void {
  clearOAuthAffiliateCode()
  clearAffiliateReferralCode()
}

export function oauthAffiliatePayload(value?: unknown): { aff_code?: string } {
  const code = normalizeOAuthAffiliateCode(value)
  return code ? { aff_code: code } : {}
}
