// The person's code colours: which theme the syntax colours of every code block use, in dark mode and in
// light mode, and any colour they changed on top. Kept on the runner per account (GET/PUT
// /api/me/preferences) and cached in localStorage so the first paint already uses the last choice; the
// server's value wins when it arrives. The tokens themselves are the chat package's (theme/codeThemes).
import { useEffect, useSyncExternalStore } from 'react'
import {
  DEFAULT_CODE_COLOURS, applyCodeColours, currentCodeMode, normaliseCodeColours, type CodeColours,
} from '@blerglab/chat'
import { apiFetch } from '../apiFetch'

export const CODE_COLOURS_KEY = 'blerg-runner.code-colours'

function readStored(): CodeColours {
  try {
    const raw = localStorage.getItem(CODE_COLOURS_KEY)
    return raw ? normaliseCodeColours(JSON.parse(raw)) : DEFAULT_CODE_COLOURS
  } catch {
    return DEFAULT_CODE_COLOURS
  }
}

let current: CodeColours = readStored()
const listeners = new Set<() => void>()

export const getCodeColours = (): CodeColours => current
const subscribe = (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn) } }

/** Paint the current choice on the page: the root element, in the mode the page shows now. */
export function applyCodeColoursToPage(): void {
  applyCodeColours(document.documentElement, current, currentCodeMode())
}

/** Make `c` the choice: remembered on this browser, painted, and announced to the hooks. */
export function setCodeColours(c: CodeColours): void {
  current = c
  try { localStorage.setItem(CODE_COLOURS_KEY, JSON.stringify(c)) } catch { /* private mode: this page only */ }
  applyCodeColoursToPage()
  listeners.forEach(fn => fn())
}

/** The server's choice. When the server answers but holds none, the defaults: a cached choice left by
 *  another account on this browser must not outlive the answer. Null only when it could not be read. */
export async function fetchCodeColours(): Promise<CodeColours | null> {
  try {
    const r = await apiFetch('/api/me/preferences')
    if (!r.ok) return null
    const body = (await r.json()) as { code_colours?: unknown }
    return body.code_colours ? normaliseCodeColours(body.code_colours) : DEFAULT_CODE_COLOURS
  } catch {
    return null
  }
}

/** Save `c` on the server; on success it becomes the choice. Returns an error sentence, or '' on success. */
export async function saveCodeColours(c: CodeColours): Promise<string> {
  try {
    const r = await apiFetch('/api/me/preferences', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ code_colours: c }),
    })
    if (!r.ok) {
      const msg = await r.json().then((b: { error?: string }) => b.error).catch(() => '')
      return msg || `Could not save (${r.status})`
    }
  } catch {
    return 'Could not reach the server'
  }
  setCodeColours(c)
  return ''
}

export function useCodeColours(): CodeColours {
  return useSyncExternalStore(subscribe, getCodeColours, getCodeColours)
}

/** Mounted once by the app: paints the cached choice, repaints when the page's mode changes, and
 *  takes the server's choice when it arrives. */
export function useCodeColoursSync(): void {
  useEffect(() => {
    applyCodeColoursToPage()
    const obs = new MutationObserver(applyCodeColoursToPage)
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    const mq = window.matchMedia?.('(prefers-color-scheme: light)')
    mq?.addEventListener?.('change', applyCodeColoursToPage)
    let cancelled = false
    void fetchCodeColours().then(c => {
      if (!cancelled && c && JSON.stringify(c) !== JSON.stringify(current)) setCodeColours(c)
    })
    return () => {
      cancelled = true
      obs.disconnect()
      mq?.removeEventListener?.('change', applyCodeColoursToPage)
    }
  }, [])
}
