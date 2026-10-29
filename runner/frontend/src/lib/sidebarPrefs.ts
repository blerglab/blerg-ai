// Tiny localStorage helpers for sidebar UI preferences.
// Keys are stable across releases.
//
// All reads tolerate missing or malformed JSON by returning safe defaults.

import { useSyncExternalStore } from 'react'

const FOCUS_KEY = 'blerg-runner.sidebar.focus'

/**
 * Returns the current sidebar focus, or null for "All": a workstation daemon
 * id, or the string "cluster" for the cluster runtime.
 */
export function getFocus(): string | null {
  try {
    const raw = localStorage.getItem(FOCUS_KEY)
    if (raw === null) return null
    const parsed = JSON.parse(raw)
    return typeof parsed === 'string' ? parsed : null
  } catch {
    return null
  }
}

/** Persists the sidebar focus. Pass null to clear (show All). */
export function setFocus(id: string | null): void {
  if (id === null) {
    localStorage.removeItem(FOCUS_KEY)
  } else {
    localStorage.setItem(FOCUS_KEY, JSON.stringify(id))
  }
}

const CHILDREN_KEY = 'blerg-runner.sidebar.childrenClosed'
const childrenListeners = new Set<() => void>()
const childrenFallback = new Map<string, boolean>()
const unsavedChildren = new Set<string>()
function subscribeChildrenPrefs(listener: () => void) {
  childrenListeners.add(listener)
  window.addEventListener('storage', listener)
  return () => { childrenListeners.delete(listener); window.removeEventListener('storage', listener) }
}

/** Cards and tile previews share one per-parent choice without waking unrelated branches. */
export function useChildrenExpanded(parent: string): boolean {
  return useSyncExternalStore(subscribeChildrenPrefs, () => childrenExpanded(parent), () => true)
}
export function childrenExpanded(parent: string): boolean {
  if (unsavedChildren.has(parent)) return childrenFallback.get(parent) ?? true
  try { return !(JSON.parse(localStorage.getItem(CHILDREN_KEY) ?? '[]') as unknown[]).includes(parent) } catch { return childrenFallback.get(parent) ?? true }
}
export function setChildrenExpanded(parent: string, expanded: boolean): void {
  childrenFallback.set(parent, expanded)
  try {
    const saved: unknown = JSON.parse(localStorage.getItem(CHILDREN_KEY) ?? '[]')
    const closed = new Set(Array.isArray(saved) ? saved.filter((v): v is string => typeof v === 'string') : [])
    if (expanded) closed.delete(parent); else closed.add(parent)
    localStorage.setItem(CHILDREN_KEY, JSON.stringify([...closed].slice(-200)))
    unsavedChildren.delete(parent)
  } catch { unsavedChildren.add(parent) }
  childrenListeners.forEach(listener => listener())
}
