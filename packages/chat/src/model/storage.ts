// Where the chat keeps a person's small conveniences: the unsent draft of each session and whether
// tool calls are folded. An app may hand ChatView its own ChatStorage; the default is localStorage
// behind try/catch (a private window, blocked site data: nothing depends on it), with a memory
// fallback so the choice still holds for the life of the page.

export interface ChatStorage {
  get(key: string): string | null
  /** null removes the entry. */
  set(key: string, value: string | null): void
}

const memory = new Map<string, string>()

export const localStorageChatStorage: ChatStorage = {
  get(key) {
    try {
      const v = localStorage.getItem(key)
      if (v !== null) return v
    } catch { /* storage unavailable: the memory copy stands */ }
    return memory.get(key) ?? null
  },
  set(key, value) {
    if (value === null) memory.delete(key)
    else memory.set(key, value)
    try {
      if (value === null) localStorage.removeItem(key)
      else localStorage.setItem(key, value)
    } catch { /* see above */ }
  },
}

// The keys are the runner's from before the package, so nobody's draft or setting is lost.
export const COMPACT_KEY = 'blerg.agent.compactTools'
const DRAFT_PREFIX = 'blerg-runner.draft.'

export function getDraft(storage: ChatStorage, sessionId: string): string {
  return storage.get(DRAFT_PREFIX + sessionId) ?? ''
}

/** Saves the draft; an empty one clears the entry. */
export function setDraft(storage: ChatStorage, sessionId: string, text: string): void {
  storage.set(DRAFT_PREFIX + sessionId, text === '' ? null : text)
}

/** Compact tool calls are on unless the person turned them off. */
export function loadCompact(storage: ChatStorage): boolean {
  return storage.get(COMPACT_KEY) !== '0'
}

export function saveCompact(storage: ChatStorage, on: boolean): void {
  storage.set(COMPACT_KEY, on ? '1' : '0')
}
