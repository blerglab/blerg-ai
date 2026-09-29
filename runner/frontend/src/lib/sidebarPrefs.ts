// Tiny localStorage helpers for sidebar UI preferences.
// Keys are stable across releases — tasks 8+ may add to this module.
//
// All reads tolerate missing or malformed JSON by returning safe defaults.

const FOCUS_KEY = 'blerg-runner.sidebar.focus'
const COLLAPSED_KEY = 'blerg-runner.sidebar.collapsed'

/** Returns the currently focused daemon ID, or null for "All". */
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

/** Persists the focused daemon ID. Pass null to clear (show All). */
export function setFocus(id: string | null): void {
  if (id === null) {
    localStorage.removeItem(FOCUS_KEY)
  } else {
    localStorage.setItem(FOCUS_KEY, JSON.stringify(id))
  }
}

/**
 * Returns the persisted collapsed map (daemonId → true means collapsed).
 * Missing entries mean "not yet set" — callers should treat them as expanded.
 */
export function getCollapsed(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(COLLAPSED_KEY)
    if (raw === null) return {}
    const parsed = JSON.parse(raw)
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return {}
    return parsed as Record<string, boolean>
  } catch {
    return {}
  }
}

/** Persists the full collapsed map. */
export function setCollapsed(map: Record<string, boolean>): void {
  localStorage.setItem(COLLAPSED_KEY, JSON.stringify(map))
}
