export type ColorTheme = 'light' | 'dark'

export function resolveSavedTheme(savedTheme = localStorage.getItem('theme')): ColorTheme {
  return savedTheme === 'dark' ? 'dark' : 'light'
}

export function applySavedTheme(): boolean {
  const isDark = resolveSavedTheme() === 'dark'
  document.documentElement.classList.toggle('dark', isDark)
  return isDark
}
