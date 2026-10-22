// Folder files a page reads (a document's images, the folder viewer's own file) go through one queue
// per page: at most two reads in flight, so a README with many images neither trips the server's
// workspace-read limit nor starves another person's viewer.
//
// A refused read is retried by the server's machine-readable reason (the JSON `code` beside its
// sentence, runner/internal/server/workspacefile.go), never by the sentence:
//   - `busy`: every read slot is taken. Answered at once and holding nothing, so a document's image
//     keeps trying for as long as the document is open (untilAborted).
//   - `daemon_timeout` (the daemon's reply did not arrive) and `changed` (a reconnect while
//     opening): these can last, and each try holds a server slot for up to its 10 s timeout, so
//     they get FEW_TRIES and then the image offers Retry.
//   - anything else, a 409 without a code included, is final.
// The delay between tries is one backoff for the whole page, not one per image: after a refusal
// every read waits, so forty images do not each probe the server. Retries pause while the tab is
// hidden and resume when it is shown again.

/** Reads in flight at once, for the whole page. */
export const MAX_IN_FLIGHT = 2
/** Tries per read when the server is busy and the caller does not wait until it closes. */
export const MAX_TRIES = 8
/** Tries per read when the daemon's reply did not arrive, or the workspace changed. */
export const FEW_TRIES = 3
const BASE_DELAY_MS = 250
/** The longest wait between two tries. */
export const MAX_DELAY_MS = 4000

let active = 0
const waiting: Array<() => void> = []
// The page's shared backoff: no read starts before holdUntil; each refusal in a row doubles it.
let holdUntil = 0
let refusals = 0

function abortError(): Error {
  return typeof DOMException !== 'undefined' ? new DOMException('Aborted', 'AbortError') : new Error('Aborted')
}

function acquire(signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) return Promise.reject(abortError())
  if (active < MAX_IN_FLIGHT) {
    active++
    return Promise.resolve()
  }
  return new Promise<void>((resolve, reject) => {
    const go = () => {
      signal?.removeEventListener('abort', onAbort)
      active++
      resolve()
    }
    const onAbort = () => {
      const at = waiting.indexOf(go)
      if (at >= 0) waiting.splice(at, 1)
      reject(abortError())
    }
    waiting.push(go)
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

function release() {
  active--
  const next = waiting.shift()
  if (next) next()
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) { reject(abortError()); return }
    const t = setTimeout(() => { signal?.removeEventListener('abort', onAbort); resolve() }, ms)
    const onAbort = () => { clearTimeout(t); reject(abortError()) }
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

function hidden(): boolean {
  return typeof document !== 'undefined' && document.visibilityState === 'hidden'
}

// Resolves when the tab is visible (at once if it is).
function whenVisible(signal?: AbortSignal): Promise<void> {
  if (!hidden()) return Promise.resolve()
  return new Promise((resolve, reject) => {
    const onChange = () => { if (!hidden()) { done(); resolve() } }
    const onAbort = () => { done(); reject(abortError()) }
    const done = () => {
      document.removeEventListener('visibilitychange', onChange)
      signal?.removeEventListener('abort', onAbort)
    }
    document.addEventListener('visibilitychange', onChange)
    signal?.addEventListener('abort', onAbort, { once: true })
  })
}

// Waits out the page's backoff (every read, first ones included) and, before a retry, a hidden tab.
async function tryAllowed(retry: boolean, signal?: AbortSignal): Promise<void> {
  for (;;) {
    if (retry) await whenVisible(signal)
    const wait = holdUntil - Date.now()
    if (wait <= 0) return
    await sleep(wait, signal)
  }
}

type Refusal = 'busy' | 'few' | 'final'

function refusal(e: unknown): Refusal {
  if (typeof e !== 'object' || e === null) return 'final'
  const { status, code } = e as { status?: unknown; code?: unknown }
  if (status !== 409) return 'final'
  if (code === 'busy') return 'busy'
  if (code === 'daemon_timeout' || code === 'changed') return 'few'
  return 'final'
}

export interface QueueOptions {
  /** Keep trying while the server is busy for as long as the caller waits (until `signal` aborts):
   *  a document's images never give up while the server is only busy. */
  untilAborted?: boolean
  /** Told about each refusal that is tried again (the folder viewer keeps the server's reason). */
  onRefused?: (e: unknown) => void
}

/** Runs `read` in the page's queue, retrying the refusals that pass (see the top of this file). */
export async function queuedRead<T>(read: (signal?: AbortSignal) => Promise<T>, signal?: AbortSignal, opts: QueueOptions = {}): Promise<T> {
  let busyTries = 0
  let fewTries = 0
  for (let attempt = 1; ; attempt++) {
    await tryAllowed(attempt > 1, signal)
    await acquire(signal)
    let error: unknown
    try {
      const out = await read(signal)
      refusals = 0
      return out
    } catch (e) {
      error = e
    } finally {
      release()
    }
    if (signal?.aborted) throw error
    const kind = refusal(error)
    if (kind === 'final') throw error
    if (kind === 'busy' && ++busyTries >= MAX_TRIES && !opts.untilAborted) throw error
    if (kind === 'few' && ++fewTries >= FEW_TRIES) throw error
    opts.onRefused?.(error)
    refusals++
    const delay = Math.min(MAX_DELAY_MS, BASE_DELAY_MS * 2 ** Math.min(refusals - 1, 10))
    holdUntil = Math.max(holdUntil, Date.now() + delay + Math.floor(Math.random() * BASE_DELAY_MS))
  }
}

/** Reads in flight and waiting (tests). */
export function imageQueueState(): { active: number; waiting: number } {
  return { active, waiting: waiting.length }
}

/** Forgets the page's backoff (tests). */
export function resetImageQueueBackoff(): void {
  holdUntil = 0
  refusals = 0
}
