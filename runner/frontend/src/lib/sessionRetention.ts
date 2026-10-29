// sessionRetention: what the page keeps of the sessions viewed most recently so a return draws
// them at once (card #281): the chat transcript (the chat's transcript store already resumes from
// it), the Files list and open file contents (@blerglab/chat viewCache), shell terminals
// (lib/keptTerminals, their own tighter bound) and the shells' kept transports and scrollback
// (shellTransport). The explorer and panel cache (#250, lib/sessionViewCache) follows the same rules.
//
// - Bounded: the last KEPT_SESSIONS sessions viewed; an older one's transcript, Files list,
//   contents, terminals and shell scrollback are forgotten (a later visit loads them as a first
//   visit does).
// - One sign-in's: another account, a sign-in change (another login session of the same account),
//   a confirmed sign-out or a desktop scope change forgets everything; the file cache moves to a new
//   epoch, so a read still out keeps nothing.
// - Only live sessions: one that ended, was archived or discarded is forgotten, and nothing more is
//   kept for it. What the view showing it now still draws is forgotten as soon as it leaves.
import { clearViewCache, forgetViewCacheSession, useTranscriptStore } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { disposeKeptTerminals } from './keptTerminals'
import { forgetRetainedSession } from '../shellTransport'

/** How many sessions' transcripts, Files lists and file contents are kept. */
export const KEPT_SESSIONS = 10

let viewedFor = ''
/** Least recently viewed first. */
let viewed: string[] = []
const shown = new Map<string, number>()
/** Sessions forgotten while on screen: what their view still draws goes when it leaves. */
const dropOnLeave = new Set<string>()
const ENDED = new Set(['stopped', 'ended', 'error'])

function isLive(sessionId: string): boolean {
  const s = useSessionStore.getState().sessions.find(x => x.id === sessionId)
  return !!s && !s.archived_at && !ENDED.has(s.status)
}

/** Drops what is held for a session that is not on screen. */
function release(sessionId: string) {
  useTranscriptStore.getState().clear(sessionId)
  disposeKeptTerminals(sessionId)
  forgetRetainedSession(sessionId)
}

function forget(sessionId: string, ended: boolean) {
  viewed = viewed.filter(id => id !== sessionId)
  forgetViewCacheSession(sessionId, { ended })
  if (shown.get(sessionId)) dropOnLeave.add(sessionId)
  else release(sessionId)
}

function forgetAll() {
  for (const id of viewed) {
    if (shown.get(id)) dropOnLeave.add(id)
    else release(id)
  }
  viewed = []
  clearViewCache()
  disposeKeptTerminals()
}

/** A session view is on screen: it becomes the most recently viewed. Returns its leaving. */
export function sessionShown(account: string, sessionId: string): () => void {
  if (account !== viewedFor) { if (viewedFor) forgetAll(); viewedFor = account }
  shown.set(sessionId, (shown.get(sessionId) ?? 0) + 1)
  if (isLive(sessionId)) {
    dropOnLeave.delete(sessionId)
    viewed = [...viewed.filter(id => id !== sessionId), sessionId]
  } else {
    // Already over: nothing of it is kept past this visit.
    forgetViewCacheSession(sessionId, { ended: true })
    dropOnLeave.add(sessionId)
  }
  while (viewed.length > KEPT_SESSIONS) {
    const oldest = viewed.find(id => !shown.get(id))
    if (!oldest) break
    forget(oldest, false)
  }
  return () => {
    const n = (shown.get(sessionId) ?? 1) - 1
    if (n > 0) { shown.set(sessionId, n); return }
    shown.delete(sessionId)
    if (!dropOnLeave.has(sessionId)) return
    // After the view's own cleanups (a shell view keeps its terminal as it unmounts).
    queueMicrotask(() => {
      if (shown.get(sessionId) || !dropOnLeave.delete(sessionId)) return
      release(sessionId)
    })
  }
}

/** The sessions viewed and kept now, least recently viewed first (tests). */
export function retainedSessions(): string[] {
  return [...viewed]
}

/** Tests: forget what was viewed. */
export function resetSessionRetentionForTest(): void {
  viewed = []
  viewedFor = ''
  shown.clear()
  dropOnLeave.clear()
}

useSessionStore.subscribe((state, previous) => {
  if (state.sessions === previous.sessions) return
  const live = new Set(state.sessions.filter(s => !s.archived_at && !ENDED.has(s.status)).map(s => s.id))
  for (const id of [...viewed]) if (!live.has(id)) forget(id, true)
})
if (typeof window !== 'undefined') {
  for (const event of ['blerg-identity-changed', 'blerg-login-changed', 'blerg-signed-out']) window.addEventListener(event, () => { forgetAll(); viewedFor = '' })
  window.addEventListener('blerg-desktop-scope-changed', () => forgetAll())
}
