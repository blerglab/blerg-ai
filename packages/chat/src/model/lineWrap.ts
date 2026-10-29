// Line wrapping in the code viewer and the diff (#265): on by default for prose files (Markdown,
// plain text, reStructuredText, and README/CHANGELOG/LICENSE-like names), off for code. The
// person's choice is kept per account and per kind, so every viewer of that kind follows it.
import { createContext, useCallback, useContext, useEffect, useSyncExternalStore } from 'react'
import { localStorageChatStorage } from './storage'

export type WrapKind = 'prose' | 'code'

const PROSE_EXT = /\.(md|markdown|mdown|mkd|txt|text|rst)$/i
// README, CHANGELOG, LICENSE and their kin, bare or with a prose extension (CHANGES.patch is a patch).
const PROSE_NAME = /^(readme|changelog|changes|history|news|license|licence|copying|notice|authors|contributors|contributing|code_of_conduct)(\.(md|markdown|txt|rst))?$/i
// A license with its kind in the name: LICENSE-MIT, LICENSE.apache, COPYING.LESSER.
const LICENSE_NAME = /^(license|licence|copying)[-_.](?!(js|mjs|ts|tsx|jsx|go|py|rs|java|kt|c|h|cc|cpp|rb|php|sh|json|ya?ml|toml)$)[a-z0-9.-]+$/i

/** Whether a file reads as prose (wraps by default) or as code (does not). */
export function wrapKind(name: string): WrapKind {
  const base = name.split('/').pop() ?? name
  return PROSE_EXT.test(base) || PROSE_NAME.test(base) || LICENSE_NAME.test(base) ? 'prose' : 'code'
}

/** A diff is prose only when every file in it is. */
export function wrapKindOf(names: readonly string[]): WrapKind {
  return names.length > 0 && names.every(name => wrapKind(name) === 'prose') ? 'prose' : 'code'
}

const KEY = (kind: WrapKind, scope: string) => `blerg.chat.wrap.${kind}.${encodeURIComponent(scope)}`
const listeners = new Set<() => void>()
const subscribe = (listener: () => void) => { listeners.add(listener); return () => { listeners.delete(listener) } }

// Without an account scope nothing is stored on the device (another account may sign in on the
// same origin): the choice holds in this page's memory only.
const unscoped = new Map<WrapKind, boolean>()
export function lineWrap(kind: WrapKind, scope = ''): boolean {
  if (!scope) return unscoped.get(kind) ?? kind === 'prose'
  const saved = localStorageChatStorage.get(KEY(kind, scope))
  return saved === null ? kind === 'prose' : saved === '1'
}
const written = new Set<string>()
export function setLineWrap(kind: WrapKind, on: boolean, scope = '') {
  if (!scope) unscoped.set(kind, on)
  else {
    written.add(KEY(kind, scope))
    localStorageChatStorage.set(KEY(kind, scope), on ? '1' : '0')
  }
  for (const listener of listeners) listener()
}

/** The signed-in account's namespace for the setting (a viewer provides its transport's scope). */
export const WrapScopeContext = createContext('')

/** The wrap setting for `kind` for the signed-in account, and its toggle. */
export function useLineWrap(kind: WrapKind): [boolean, () => void] {
  const scope = useContext(WrapScopeContext)
  const on = useSyncExternalStore(subscribe, () => lineWrap(kind, scope), () => kind === 'prose')
  const toggle = useCallback(() => setLineWrap(kind, !lineWrap(kind, scope), scope), [kind, scope])
  return [on, toggle]
}

/** Alt+Z (Option+Z), VS Code's toggle; never while typing in a field. */
export function isWrapShortcut(e: { altKey: boolean; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; code?: string; key: string; target: EventTarget | null }): boolean {
  if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return false
  if (e.code !== 'KeyZ' && e.key.toLowerCase() !== 'z') return false
  const t = e.target as HTMLElement | null
  return !(t && (t.tagName === 'TEXTAREA' || t.tagName === 'INPUT' || t.isContentEditable))
}

/** Clicks the wrap toggle inside `root` (the focused viewer): Alt+Z and the palette command use it. */
export function toggleWrapIn(root: ParentNode | null | undefined): boolean {
  const button = root?.querySelector<HTMLButtonElement>('[data-wrap-toggle]:not([disabled])')
  if (!button) return false
  button.click()
  return true
}

/** A viewer's toolbar shows the wrap toggle while it draws code: the code tells it which kind. */
export type WrapTarget = { kind: WrapKind; available: boolean }
export const WrapHostContext = createContext<((target: WrapTarget | null) => void) | null>(null)

/** A file longer than this does not wrap: its toggle is off and disabled, whatever the saved choice
 *  (wrapped, a long file reflows slowly and loses the reader's place on every resize). */
export const WRAP_MAX_LINES = 5000
export const WRAP_UNAVAILABLE = 'Wrap is off for files over 5,000 lines'

/** For a code view of `lines` lines: whether it wraps, announced to the viewer around it for the
 *  toolbar toggle (disabled for a long file). */
export function useCodeWrap(kind: WrapKind, lines: number): boolean {
  const register = useContext(WrapHostContext)
  const available = lines <= WRAP_MAX_LINES
  useEffect(() => { register?.({ kind, available }); return () => register?.(null) }, [register, kind, available])
  return useLineWrap(kind)[0] && available
}

/** Test hook: forget every choice made in this page (storage keeps a memory copy). */
export function resetLineWrap() {
  for (const key of written) localStorageChatStorage.set(key, null)
  written.clear()
  unscoped.clear()
  for (const listener of listeners) listener()
}
