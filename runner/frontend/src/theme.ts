// Theme: dark is Blerg's default; the viewer can pin light or dark, and the
// choice persists. When nothing is pinned the OS preference (prefers-color-scheme)
// decides. The no-flash <script> in index.html applies a stored choice before
// first paint; this module is the runtime toggle.
export type Theme = 'light' | 'dark'
const KEY = 'blerg-theme'

// The effective theme right now: an explicit data-theme wins, else the OS.
export function effectiveTheme(): Theme {
  const pinned = document.documentElement.getAttribute('data-theme')
  if (pinned === 'light' || pinned === 'dark') return pinned
  return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}

export function setTheme(t: Theme) {
  document.documentElement.setAttribute('data-theme', t)
  try { localStorage.setItem(KEY, t) } catch { /* private mode — session-only */ }
}

export function toggleTheme(): Theme {
  const next: Theme = effectiveTheme() === 'dark' ? 'light' : 'dark'
  setTheme(next)
  return next
}
