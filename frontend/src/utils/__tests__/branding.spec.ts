import { beforeEach, describe, expect, it } from 'vitest'
import { resolveDisplaySiteName, updateFavicon } from '@/utils/branding'

describe('resolveDisplaySiteName', () => {
  it('maps the legacy default brand to BirdAPI', () => {
    expect(resolveDisplaySiteName()).toBe('BirdAPI')
    expect(resolveDisplaySiteName('Sub2API')).toBe('BirdAPI')
    expect(resolveDisplaySiteName(' sub2api ')).toBe('BirdAPI')
  })

  it('preserves an explicitly configured site name', () => {
    expect(resolveDisplaySiteName('Acme Gateway')).toBe('Acme Gateway')
  })
})

describe('updateFavicon', () => {
  beforeEach(() => {
    document.head.innerHTML = '<link rel="icon" href="/logo.svg">'
  })

  it('replaces the default favicon with the configured logo', () => {
    updateFavicon('https://example.com/custom-logo.png')

    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(link?.href).toBe('https://example.com/custom-logo.png')
  })

  it('ignores unsafe logo URLs', () => {
    updateFavicon('javascript:alert(1)')

    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(link?.getAttribute('href')).toBe('/logo.svg')
  })
})
