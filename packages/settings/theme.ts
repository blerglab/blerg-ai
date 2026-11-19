/** Shared first-party account theme vocabulary. The two default IDs retain old caches.
 * The five bundled schemes after high-contrast (BLG-474) come from scripts/themeimport; see
 * scripts/themeimport/manifest.go for each one's confirmed license and source. Gruvbox and
 * Solarized were considered and left out: no vendoring-grade LICENSE text was reachable for
 * either (manifest.go has the detail). */
export const THEMES = [
  { id: 'system', name: 'Follow system' },
  { id: 'dark', name: 'Default dark' },
  { id: 'light', name: 'Default light' },
  { id: 'colorblind-dark', name: 'Color-blind dark' },
  { id: 'colorblind-light', name: 'Color-blind light' },
  { id: 'high-contrast', name: 'High contrast' },
  { id: 'catppuccin-mocha', name: 'Catppuccin Mocha' },
  { id: 'catppuccin-latte', name: 'Catppuccin Latte' },
  { id: 'dracula', name: 'Dracula' },
  { id: 'nord', name: 'Nord' },
  { id: 'tokyo-night', name: 'Tokyo Night' },
] as const
export type Theme = typeof THEMES[number]['id']
export function isTheme(value: unknown): value is Theme { return THEMES.some(theme => theme.id === value) }
export function localTheme(): Theme | undefined {
  try { const pin = localStorage.getItem('blerg-theme'); return isTheme(pin) ? pin : undefined } catch { return undefined }
}
export function effectiveTheme(): Theme {
  const pinned = document.documentElement.getAttribute('data-theme')
  if (isTheme(pinned)) return pinned
  return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}
export function setTheme(theme: Theme): void {
  if (theme === 'system') document.documentElement.removeAttribute('data-theme')
  else document.documentElement.setAttribute('data-theme', theme)
  try { localStorage.setItem('blerg-theme', theme) } catch { /* session-only when storage is blocked */ }
}
export function clearTheme(): void {
  accountEpoch++
  document.documentElement.removeAttribute('data-theme')
  try { localStorage.removeItem('blerg-theme') } catch { /* storage is optional */ }
}

/** The cache is an appearance hint, never a preference inherited from another account.
 * Legacy unowned pins may seed the first account once; account-owned pins cannot. */
let accountEpoch = 0
export function themeAccountEpoch(): number { return accountEpoch }
export function bindThemeAccount(token: string | null): void {
  let account = ''
  try { if (token) account = (JSON.parse(atob((token ?? '').split('.')[1].replace(/-/g, '+').replace(/_/g, '/'))) as {sub?: string}).sub ?? '' } catch { return }
  try {
    const previous = localStorage.getItem('blerg-theme-account')
    if (previous && previous !== account) { clearTheme(); window.dispatchEvent(new Event('blerg-theme-change')) }
    if (account) localStorage.setItem('blerg-theme-account', account)
    else localStorage.removeItem('blerg-theme-account')
  } catch { /* appearance storage is optional */ }
}
