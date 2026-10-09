// A Transport the tests drive by hand: a plain object (no React in it) whose controls let a test
// play the server — deliver an event or a typing delta, end a replay page, change the status, drop
// and restore the link, answer an older page, and hold or fail an upload. Every call into the
// component's handlers must be wrapped in act() by the TEST; the fake never does that itself.
import type { AgentEvent, AssistantTextPayload, SessionStatus } from '../types'
import type {
  ArtifactInfo,
  OlderPage,
  ReplayDone,
  SessionMeta,
  SubscribeHandlers,
  SubscribeOptions,
  Transport,
  TransportFiles,
  UploadedFile,
} from '../transport/types'

export interface FakeUpload {
  file: File
  signal?: AbortSignal
  onProgress?: (loaded: number, total: number) => void
}

export type UploadImpl = (file: File, ctl: { signal?: AbortSignal; onProgress?: (loaded: number, total: number) => void }) => Promise<UploadedFile>

export interface FakeFiles extends TransportFiles {
  /** What list() answers (newest first). An upload that finishes is added to the front. */
  items: ArtifactInfo[]
  listCalls: number
  /** Ids raw() was asked for, in order. */
  rawCalls: string[]
  downloads: string[]
  removed: string[]
  uploads: FakeUpload[]
  /** The size the server reports back for an uploaded file, by name (else the file's own). */
  sizes: Record<string, number>
  /** Replaces the default upload (which resolves at once). */
  uploadImpl: UploadImpl | null
  /** Set, download()/remove() reject with it. */
  downloadError: Error | null
  removeError: Error | null
  /** What raw() answers from now on: the bytes, or a failure for a status of 400 and over. */
  serve(body: string | Uint8Array, status?: number): void
  /** The reply an upload of `name` gets (also what the default upload resolves with). */
  created(name: string, size?: number): UploadedFile
}

export interface FakeTransportOptions {
  /** false: subscribe does not call onConnection(true) and connected() is false. */
  connected?: boolean
  meta?: Partial<SessionMeta>
  /** false: the transport has no file operations at all. */
  files?: false | { items?: ArtifactInfo[] }
  /** false: no interrupt (ChatView then shows no Stop). */
  interrupt?: boolean
  setModel?: boolean
  /** Pages loadOlder is answered with at once, in order; afterwards each call waits for answerOlder. */
  olderPages?: OlderPage[]
}

export interface OlderRequest { sessionId: string; beforeSeq: number; limit: number }
export interface SubscribeRecord { sessionId: string; afterSeq?: number; tail?: boolean }

interface Sub { sessionId: string; handlers: SubscribeHandlers }

export const DEFAULT_META: SessionMeta = {
  status: 'idle',
  engine: 'claude',
  model: 'claude-sonnet-5',
  started_at: '2026-07-26T00:00:00Z',
}

export function createFakeFiles(initial: ArtifactInfo[] = []): FakeFiles {
  let rawBody: string | Uint8Array = ''
  let rawError: Error | null = null
  let nextId = 0
  const files: FakeFiles = {
    items: [...initial],
    listCalls: 0,
    rawCalls: [],
    downloads: [],
    removed: [],
    uploads: [],
    sizes: {},
    uploadImpl: null,
    downloadError: null,
    removeError: null,
    serve(body, status = 200) {
      rawBody = body
      rawError = status >= 400 ? new Error(status === 404 ? 'This file is no longer available.' : `HTTP ${status}`) : null
    },
    created(name, size) {
      nextId++
      const id = String(nextId).padStart(32, 'a')
      return { id, name, size: size ?? files.sizes[name] ?? 10 }
    },
    async list() {
      files.listCalls++
      return [...files.items]
    },
    async raw(_sessionId, id) {
      files.rawCalls.push(id)
      if (rawError) throw rawError
      // Through Response so the blob has text() and arrayBuffer() whatever Blob the environment has.
      return new Response(rawBody as BodyInit).blob()
    },
    async download(_sessionId, id) {
      files.downloads.push(id)
      if (files.downloadError) throw files.downloadError
    },
    async remove(_sessionId, id) {
      files.removed.push(id)
      if (files.removeError) throw files.removeError
      files.items = files.items.filter(a => a.id !== id)
    },
    upload(_sessionId, file, onProgress, signal) {
      files.uploads.push({ file, signal, onProgress })
      const impl: UploadImpl = files.uploadImpl ?? (async f => {
        const up = files.created(f.name, files.sizes[f.name] ?? f.size)
        files.items = [{ ...up, content_type: file.type || 'application/octet-stream', view: 'pdf', origin: 'user' }, ...files.items]
        return up
      })
      return impl(file, { signal, onProgress })
    },
  }
  return files
}

export function createFakeTransport(opts: FakeTransportOptions = {}) {
  let connected = opts.connected !== false
  let refuse = false
  let meta: SessionMeta = { ...DEFAULT_META, ...opts.meta }
  const subs = new Set<Sub>()
  const subscriptions: SubscribeRecord[] = []
  const sent: string[] = []
  const older: OlderRequest[] = []
  const olderPages = [...(opts.olderPages ?? [])]
  const pendingOlder: Array<{ resolve: (p: OlderPage) => void; reject: (e: unknown) => void }> = []
  const setModelCalls: Array<{ model?: string; effort?: string }> = []
  let interrupts = 0
  let stops = 0
  const files = opts.files === false ? undefined : createFakeFiles(opts.files?.items)

  const transport: Transport = {
    subscribe(sessionId: string, o: SubscribeOptions, handlers: SubscribeHandlers) {
      subscriptions.push({ sessionId, afterSeq: o.afterSeq, tail: o.tail })
      const sub: Sub = { sessionId, handlers }
      subs.add(sub)
      if (connected) handlers.onConnection(true)
      return () => { subs.delete(sub) }
    },
    loadOlder(sessionId, beforeSeq, limit) {
      older.push({ sessionId, beforeSeq, limit })
      return new Promise<OlderPage>((resolve, reject) => {
        const page = olderPages.shift()
        if (page) resolve(page)
        else pendingOlder.push({ resolve, reject })
      })
    },
    async session() {
      return { ...meta }
    },
    async send(_sessionId, text) {
      if (!connected || refuse) return { queued: false }
      sent.push(text)
      return { queued: true }
    },
    async stop() {
      stops++
    },
    connected: () => connected,
    files,
  }
  if (opts.interrupt !== false) transport.interrupt = async () => { interrupts++ }
  if (opts.setModel !== false) transport.setModel = async (_sessionId, model, effort) => { setModelCalls.push({ model, effort }) }

  const each = (f: (h: SubscribeHandlers) => void) => { for (const s of [...subs]) f(s.handlers) }

  return {
    transport,
    /** The file fake, or undefined when the transport has none. */
    files,
    sent,
    older,
    setModelCalls,
    subscriptions,
    get interrupts() { return interrupts },
    get stops() { return stops },
    get meta() { return meta },
    /** A persisted event to onEvent; a transient assistant_text delta to onStreaming. */
    emit(ev: AgentEvent) {
      if (ev.transient && ev.kind === 'assistant_text') {
        const text = (ev.payload as AssistantTextPayload).text
        each(h => h.onStreaming(text))
      } else {
        each(h => h.onEvent(ev))
      }
    },
    replayDone(done: Partial<ReplayDone> = {}) {
      const full: ReplayDone = { lastSeq: 0, hasMore: false, ...done }
      each(h => h.onReplayDone(full))
    },
    setStatus(status: SessionStatus) {
      each(h => h.onStatus(status))
    },
    setConnected(c: boolean) {
      connected = c
      each(h => h.onConnection(c))
    },
    /** send answers queued=false (as a transport whose socket just closed) without recording. */
    refuseSend(r: boolean) { refuse = r },
    setMeta(over: Partial<SessionMeta>) { meta = { ...meta, ...over } },
    /** Resolves the oldest loadOlder still waiting. */
    answerOlder(page: Partial<OlderPage> & { events: AgentEvent[] }) {
      const p = pendingOlder.shift()
      if (!p) throw new Error('no loadOlder is waiting')
      p.resolve({ hasOlder: false, firstSeq: page.events[0]?.seq ?? 0, ...page })
    },
    /** Fails the oldest loadOlder still waiting (the link dropped mid-page). */
    failOlder(err: Error = new Error('not connected')) {
      const p = pendingOlder.shift()
      if (!p) throw new Error('no loadOlder is waiting')
      p.reject(err)
    },
    get pendingOlder() { return pendingOlder.length },
  }
}

export type FakeTransport = ReturnType<typeof createFakeTransport>
