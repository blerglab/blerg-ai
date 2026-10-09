// The transport contract: what a chat needs from a session — what the runner's view actually reads
// today, no more (docs/design/chat-package.md, "The transport contract"). Two adapters ship with
// the package (blerg.ts over the runner's own websocket and routes, proxy.ts over an app's proxy
// of the runner's v1 contract); an app with a backend of its own shape writes a third.
import type { AgentEvent, EndedBy, SessionStatus } from '../types'
import type { ArtifactInfo } from '../model/artifacts'
import type { UploadedFile } from '../model/attachments'

// The file shapes are the model's (a list entry, the upload route's answer); named here again so
// a transport author imports everything from one place.
export type { ArtifactInfo, UploadedFile }

/** The session as a chat needs it: its live status (every lock, bubble and banner keys on it),
 *  what runs it, and when it began and ended. */
export interface SessionMeta {
  status: SessionStatus
  engine?: string // '' / absent = claude
  runtime?: string
  model?: string
  effort?: string
  started_at: string
  ended_at?: string | null
  /** Why a finished session ended (a fixed code) and who ended it; what a failed start said. All
   *  optional: a backend that does not record them gets the plain wording. */
  end_reason?: string
  ended_by?: EndedBy | null
  error_reason?: string
  message?: string | null
}

/** The end of a replay page: how far it reached and what is left on either side. A tail page
 *  (the newest events first) carries `older`, `hasOlder` and `firstSeq`; a forward page `hasMore`. */
export interface ReplayDone {
  lastSeq: number
  hasMore: boolean
  older?: boolean
  hasOlder?: boolean
  firstSeq?: number
  /** The server's clock at replay end, for skew-free elapsed times; absent when it did not say. */
  serverTime?: string
}

export interface SubscribeHandlers {
  /** A persisted event (replayed or live). The same event can arrive twice around a reconnect;
   *  the transcript dedupes by client_event_id. */
  onEvent: (ev: AgentEvent) => void
  /** A typing delta: the assistant's text so far this turn, never persisted. */
  onStreaming: (text: string) => void
  onReplayDone: (done: ReplayDone) => void
  onStatus: (status: SessionStatus) => void
  /** The link to the session came up or went down. Down locks the composer; up means the
   *  transport has resubscribed from the last seq it saw. */
  onConnection: (connected: boolean) => void
}

export interface SubscribeOptions {
  afterSeq?: number
  /** Ask for the newest page first (how the runner opens a long transcript); the older part is
   *  then paged in with loadOlder. */
  tail?: boolean
}

export interface OlderPage {
  events: AgentEvent[]
  hasOlder: boolean
  firstSeq: number
  serverTime?: string
}

export interface TransportFiles {
  list(sessionId: string): Promise<ArtifactInfo[]>
  /** The bytes through the authenticated route, never a bare URL. */
  raw(sessionId: string, id: string): Promise<Blob>
  /** Saves the file through the browser (an anchor on an object URL). */
  download(sessionId: string, id: string): Promise<void>
  remove(sessionId: string, id: string): Promise<void>
  upload(
    sessionId: string,
    file: File,
    onProgress?: (loaded: number, total: number) => void,
    signal?: AbortSignal,
  ): Promise<UploadedFile>
}

export interface Transport {
  /** Replay then live. tail=true asks for the newest page first (how the runner opens a long
   *  transcript); onReplayDone carries hasMore/hasOlder/firstSeq/lastSeq/serverTime. Returns the
   *  unsubscribe. */
  subscribe(sessionId: string, opts: SubscribeOptions, handlers: SubscribeHandlers): () => void
  /** The `limit` events just before `beforeSeq` (the next older page). */
  loadOlder(sessionId: string, beforeSeq: number, limit: number): Promise<OlderPage>
  session(sessionId: string): Promise<SessionMeta>
  /** queued=false: not connected, nothing was sent (the text stays in the box). */
  send(sessionId: string, text: string): Promise<{ queued: boolean }>
  /** Ends the session for good (the Stop in the start panel). */
  stop(sessionId: string): Promise<void>
  /** Optional: cuts the turn that is running short (the toolbar's Stop, Esc); the session goes on.
   *  Absent: ChatView shows no Stop while a turn runs. */
  interrupt?(sessionId: string): Promise<void>
  /** Optional: the composer's /model and /effort. Either may be omitted to keep the current one. */
  setModel?(sessionId: string, model?: string, effort?: string): Promise<void>
  connected(): boolean
  /** Absent: ChatView shows no Files panel and no attach control. */
  files?: TransportFiles
}
