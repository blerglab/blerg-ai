import * as React from 'react'
import { clearTheme, themeAccountEpoch, localTheme, isTheme, setTheme, THEMES, type Theme } from './theme'

interface Props {
  read: () => Promise<unknown>
  sync?: boolean
  save: (theme: Theme) => Promise<void>
}

let revision = 0
let writes = 0
let tail = Promise.resolve()
// Account seeding and explicit choices share one ordered writer. Reads cannot
// repaint during either kind of write, and rapid choices are never discarded.
function write(task: () => Promise<void>): Promise<void> {
  writes++
  const result = tail.then(task)
  tail = result.catch(() => {}).finally(() => { writes-- })
  return result
}

/** Save before painting; a failed write never looks like a synced account preference. */
export function useThemeSync(read: Props['read'], save: Props['save'], enabled = true) {
  React.useEffect(() => {
    if (!enabled) return
    let active = true
    const refresh = async () => {
      if (writes) return
      const version = ++revision
      const epoch = themeAccountEpoch()
      try {
        const body = await read() as {theme?: unknown}
        if (!active || version !== revision) return
        if (epoch !== themeAccountEpoch()) { window.dispatchEvent(new Event('blerg-theme-change')); return }
        if (isTheme(body.theme)) setTheme(body.theme)
        else if (body.theme === '') {
          const pin = localTheme()
          if (isTheme(pin)) await write(async () => {
            if (active && version === revision) await save(pin)
          })
        }
        if (active && version === revision) window.dispatchEvent(new Event('blerg-theme-change'))
      } catch { /* offline and signed-out pages retain their local pin */ }
    }
    const visible = () => { if (!document.hidden) void refresh() }
    const signedOut = () => { revision++; clearTheme(); window.dispatchEvent(new Event('blerg-theme-change')) }
    void refresh()
    window.addEventListener('focus', visible)
    window.addEventListener('blerg-auth-refreshed', visible)
    window.addEventListener('blerg-signed-out', signedOut)
    document.addEventListener('visibilitychange', visible)
    return () => {
      active = false
      window.removeEventListener('focus', visible)
      window.removeEventListener('blerg-auth-refreshed', visible)
      window.removeEventListener('blerg-signed-out', signedOut)
      document.removeEventListener('visibilitychange', visible)
    }
  }, [read, save, enabled])
}

export function ThemePicker({ read, save, sync = true }: Props) {
  useThemeSync(read, save, sync)
  const [theme, showTheme] = React.useState<Theme>(() => localTheme() ?? 'system')
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState('')
  const pending = React.useRef(0)
  React.useEffect(() => {
    const update = () => showTheme(localTheme() ?? 'system')
    const media = window.matchMedia?.('(prefers-color-scheme: light)')
    window.addEventListener('blerg-theme-change', update)
    media?.addEventListener?.('change', update)
    return () => { window.removeEventListener('blerg-theme-change', update); media?.removeEventListener?.('change', update) }
  }, [])
  const choose = async (next: Theme) => {
    revision++
    const epoch = themeAccountEpoch()
    pending.current++
    showTheme(next)
    setBusy(true); setError('')
    try {
      await write(async () => {
        if (epoch !== themeAccountEpoch()) throw new Error('Account changed')
        await save(next)
        if (epoch === themeAccountEpoch()) setTheme(next)
      })
      window.dispatchEvent(new Event('blerg-theme-change'))
    } catch { showTheme(localTheme() ?? 'system'); setError('Could not save theme. Try again.') }
    finally { pending.current--; setBusy(pending.current > 0) }
  }
  return <span style={{ display: 'inline-flex', flexWrap: 'wrap', alignItems: 'center', gap: 4, maxWidth: '100%', minWidth: 0 }}>
    <select aria-label="Theme" value={theme} aria-busy={busy}
      onKeyDown={event => {
        if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
          event.preventDefault()
          const index = THEMES.findIndex(option => option.id === theme)
          const next = event.key === 'Home' ? 0 : event.key === 'End' ? THEMES.length - 1 : Math.max(0, Math.min(THEMES.length - 1, index + (event.key === 'ArrowDown' ? 1 : -1)))
          showTheme(THEMES[next].id)
        } else if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault(); void choose(theme)
        } else if (event.key === 'Escape') { event.preventDefault(); showTheme(localTheme() ?? 'system') }
      }}
      onBlur={() => showTheme(localTheme() ?? 'system')}
      onChange={event => { if (isTheme(event.target.value)) void choose(event.target.value) }}
      style={{ font: 'inherit', fontSize: 12, maxWidth: '100%', minWidth: 0, padding: '4px 6px', borderRadius: 6, color: 'var(--fog)', background: 'var(--scree)', border: '1px solid var(--stone)' }}>
      {THEMES.map(option => <option key={option.id} value={option.id}>{option.name}</option>)}
    </select>
    {error && <span role="alert" style={{ fontSize: 12, color: 'var(--danger)' }}>{error}</span>}
  </span>
}
