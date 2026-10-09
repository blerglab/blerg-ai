// Shared bits of the ChatView suites: an event factory, a session row, the transcript seeding the
// runner's tests did through its store, and the reset every test starts from.
import { useTranscriptStore } from '../hooks/useTranscriptStore'
import { resetKeptAttachments } from '../hooks/useAttachments'
import { clearPreviewCache } from '../model/artifactPreview'
import { COMPACT_KEY, localStorageChatStorage, type ChatStorage } from '../model/storage'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent } from '../types'

export const SESSION = 's1'
export const COMPACT = COMPACT_KEY

let counter = 0

/** An event of session s1; every field can be overridden. A fresh client_event_id each time. */
export function ev(over: Omit<Partial<AgentEvent>, 'kind'> & { kind?: string } = {}): AgentEvent {
  counter++
  return {
    type: 'agent_event',
    session_id: SESSION,
    client_event_id: `e${counter}`,
    ts: '2026-07-26T00:00:00Z',
    kind: 'user_message',
    payload: {},
    ...over,
  } as AgentEvent
}

export function meta(over: Partial<SessionMeta> = {}): SessionMeta {
  return { status: 'idle', engine: 'claude', model: 'claude-sonnet-5', started_at: '2026-07-26T00:00:00Z', ...over }
}

/** Puts events straight into the store (as a replay would) and marks the replay complete. */
export function seed(...events: AgentEvent[]) {
  const s = useTranscriptStore.getState()
  let last = 0
  for (const e of events) {
    s.ingest(e)
    last = Math.max(last, e.seq ?? 0)
  }
  s.ingestReplayDone(SESSION, { lastSeq: last, hasMore: false })
}

/** Like seed, but leaves the replay open (the skeleton stays up). */
export function ingest(...events: AgentEvent[]) {
  const s = useTranscriptStore.getState()
  for (const e of events) s.ingest(e)
}

/** Forgets everything one test can leave behind for the next. */
export function resetChatState() {
  counter = 0
  useTranscriptStore.setState({ sessions: {} })
  resetKeptAttachments()
  clearPreviewCache()
  localStorageChatStorage.set(COMPACT_KEY, null)
  localStorageChatStorage.set(`blerg-runner.draft.${SESSION}`, null)
}

/** A ChatStorage that lives for one test. */
export function memStorage(): ChatStorage & { map: Map<string, string> } {
  const map = new Map<string, string>()
  return {
    map,
    get: k => map.get(k) ?? null,
    set: (k, v) => { if (v === null) map.delete(k); else map.set(k, v) },
  }
}

export function file(name: string, size = 10, type = 'application/pdf'): File {
  return new File([new Uint8Array(Math.min(size, 16))], name, { type })
}

export function bigFile(name: string, size: number): File {
  const f = file(name)
  Object.defineProperty(f, 'size', { value: size })
  return f
}
