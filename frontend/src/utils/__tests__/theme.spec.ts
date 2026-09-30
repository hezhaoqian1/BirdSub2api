import { beforeEach, describe, expect, it } from 'vitest'
import { applySavedTheme, resolveSavedTheme } from '@/utils/theme'

describe('theme preference', () => {
  beforeEach(() => {
    localStorage.clear()
    document.documentElement.classList.remove('dark')
  })

  it('uses light mode when no preference has been saved', () => {
    expect(resolveSavedTheme()).toBe('light')
    expect(applySavedTheme()).toBe(false)
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })

  it('restores an explicitly saved dark preference', () => {
    localStorage.setItem('theme', 'dark')

    expect(resolveSavedTheme()).toBe('dark')
    expect(applySavedTheme()).toBe(true)
    expect(document.documentElement.classList.contains('dark')).toBe(true)
  })

  it('treats an explicitly saved light preference as light mode', () => {
    localStorage.setItem('theme', 'light')
    document.documentElement.classList.add('dark')

    expect(applySavedTheme()).toBe(false)
    expect(document.documentElement.classList.contains('dark')).toBe(false)
  })
})
