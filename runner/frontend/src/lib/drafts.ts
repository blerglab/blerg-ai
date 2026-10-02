// Unsent chat messages, kept per session so leaving a session and coming back
// (or reloading) does not lose what was typed. Storage failures are ignored:
// a draft is a convenience, never state anything depends on.

const PREFIX = 'blerg-runner.draft.'
const memory = new Map<string, string>()

export function getDraft(sessionId: string): string {
  try {
    const stored = localStorage.getItem(PREFIX + sessionId)
    if (stored !== null) return stored
  } catch { /* storage unavailable: fall back to memory */ }
  return memory.get(sessionId) ?? ''
}

/** Saves the draft; an empty one clears the entry. */
export function setDraft(sessionId: string, text: string): void {
  if (text === '') memory.delete(sessionId)
  else memory.set(sessionId, text)
  try {
    if (text === '') localStorage.removeItem(PREFIX + sessionId)
    else localStorage.setItem(PREFIX + sessionId, text)
  } catch { /* see above */ }
}
