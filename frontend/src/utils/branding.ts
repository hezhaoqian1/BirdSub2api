import { sanitizeUrl } from '@/utils/url'

export function resolveDisplaySiteName(siteName?: string | null): string {
  const normalizedName = siteName?.trim()
  if (!normalizedName || normalizedName.toLowerCase() === 'sub2api') {
    return 'BirdAPI'
  }
  return normalizedName
}

export function updateFavicon(logoUrl: string): void {
  const sanitizedLogoUrl = sanitizeUrl(logoUrl, {
    allowRelative: true,
    allowDataUrl: true,
  })
  if (!sanitizedLogoUrl) {
    return
  }

  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }

  link.type = sanitizedLogoUrl.endsWith('.svg') ? 'image/svg+xml' : 'image/x-icon'
  link.href = sanitizedLogoUrl
}
