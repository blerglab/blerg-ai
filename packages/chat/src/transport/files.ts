// The session-file operations both adapters share: the routes have one shape on the runner's
// browser API and on the proxy contract (list, raw, download, delete, upload), only the prefix and
// the authentication differ. Ported from the runner's lib/artifacts.ts, lib/attachments.ts and
// apiFetch.ts; the pure helpers (grouping, versions, size text) live in the model, not here.
import type { ArtifactInfo, CommitDetails, CommitDiff, TransportFiles, UploadedFile, WorkspaceFile, WorkspaceGitRef } from './types'
import { imageBlobType } from '../model/markdownHtml'

export interface FilesOptions {
  /** Stable account/server namespace; opt in to durable local attachment queues. Never a token. */
  queueScope?: () => string
  /** The session's route prefix, e.g. `/api/sessions/<id>` or `/blerg/sessions/<id>`. */
  base: (sessionId: string) => string
  fetch?: typeof fetch
  /** Headers every request carries (the bearer token, for the runner). Asked for per request:
   *  a token is renewed silently and must be read fresh. */
  headers?: () => Promise<Record<string, string>>
  /** Looks at every response before it is read: the place to turn a 401 into a sign-out. */
  check?: (res: Response) => void
  credentials?: RequestCredentials
  /** Headers for the upload alone, when `fetch` already adds them to everything else (the upload
   *  is an XMLHttpRequest, which a wrapped fetch does not reach). Used when `headers` is absent. */
  uploadHeaders?: () => Promise<Record<string, string>>
  /** Looks at the upload's response alone, for the same reason. */
  checkUpload?: (res: Response) => void
  /** Offer `workspaceFile` (the runner's workspace-file route). Off for a proxy contract without it. */
  workspace?: boolean
  /** Offer `commit` and `commitDiff` (the runner's commit routes). Off for a proxy contract without them. */
  commits?: boolean
}

/** What a commit route takes: 7 to 64 hex digits (the server refuses anything else). */
const COMMIT_SHA = /^[0-9a-f]{7,64}$/

/** The largest commit diff read (the server's own cap, 2 MB): past it the read stops. */
export const COMMIT_DIFF_MAX = 2 << 20

function commitDetails(j: unknown): CommitDetails | null {
  const c = j as Partial<CommitDetails> | null
  if (!c || typeof c !== 'object' || typeof c.sha !== 'string' || typeof c.subject !== 'string') return null
  const num = (n: unknown) => (typeof n === 'number' && Number.isFinite(n) && n >= 0 ? n : 0)
  const str = (v: unknown) => (typeof v === 'string' ? v : '')
  return {
    ...(typeof c.member_id === 'string' && typeof c.launch_generation === 'string' && typeof c.generation === 'number' ? { member_id: c.member_id, launch_generation: c.launch_generation, generation: c.generation } : {}),
    sha: c.sha,
    short: str(c.short) || c.sha.slice(0, 7),
    subject: c.subject,
    body: str(c.body),
    ...(c.body_truncated ? { body_truncated: true } : {}),
    author: str(c.author),
    author_date: str(c.author_date),
    parents: Array.isArray(c.parents) ? c.parents.filter((p): p is string => typeof p === 'string') : [],
    files: (Array.isArray(c.files) ? c.files : [])
      .filter(f => !!f && typeof f === 'object' && typeof f.path === 'string')
      .map(f => ({ path: f.path, ...(typeof f.old_path === 'string' && f.old_path ? { old_path: f.old_path } : {}), adds: num(f.adds), dels: num(f.dels), ...(f.binary ? { binary: true } : {}) })),
    ...(c.files_truncated ? { files_truncated: true } : {}),
    adds: num(c.adds),
    dels: num(c.dels),
  }
}

const enc = encodeURIComponent

/** What reading a file says when the server answers 410: the file is still listed, its bytes are
 *  no longer stored. A viewer tells it apart from a failed read by `instanceof` (or its name). */
export const FILE_GONE_MESSAGE = 'This file is no longer available.'
export class ArtifactGoneError extends Error {
  constructor() {
    super(FILE_GONE_MESSAGE)
    this.name = 'ArtifactGoneError'
  }
}

/** True for the error raw() and download() throw on a file whose bytes are gone. */
export function isArtifactGone(e: unknown): boolean {
  return e instanceof ArtifactGoneError || (e instanceof Error && e.name === 'ArtifactGoneError')
}

/** A file request the server refused. The message is the server's own sentence when it sent one
 *  (the runner says which step failed and what to do), else a plain fallback; `errorId` is the
 *  short id the runner also wrote in its log for a failure of its own (absent from an older one). */
export class FileRequestError extends Error {
  readonly status: number
  readonly errorId?: string
  readonly code?: string
  constructor(message: string, status: number, errorId?: string, code?: string) {
    super(message)
    this.name = 'FileRequestError'
    this.code = code
    this.status = status
    if (errorId) this.errorId = errorId
  }
}

/** What to show for a failed file operation: its message (or the fallback) and the error id to
 *  show beside it, when the server gave one. */
export function fileErrorText(e: unknown, fallback: string): { message: string; errorId?: string } {
  if (!(e instanceof Error) || !e.message) return { message: fallback }
  const id = (e as { errorId?: unknown }).errorId
  return typeof id === 'string' && id ? { message: e.message, errorId: id } : { message: e.message }
}

/** fileErrorText for an action the server may refuse (delete, increase storage): the server's
 *  sentence when it refused, else a plain "couldn't reach the server" for a network failure,
 *  never a browser's "Failed to fetch". */
export function fileActionError(e: unknown, fallback: string): { message: string; errorId?: string } {
  if (e instanceof FileRequestError) return fileErrorText(e, fallback)
  if (e instanceof TypeError) return { message: 'Couldn’t reach the server. Check your connection and try again.' }
  return { message: fallback }
}

/** text without control characters (a proxy's ANSI colours and the like); whitespace is kept. */
function withoutControls(text: string): string {
  let out = ''
  for (const ch of text) {
    const n = ch.charCodeAt(0)
    if ((n < 32 && n !== 9 && n !== 10 && n !== 13) || (n >= 127 && n < 160)) continue
    out += ch
  }
  return out
}

/** The sentence and error id of an error reply: the runner's JSON `{error, error_id}`, or the
 *  text of one that is not JSON (an app's proxy answers `http.Error` in plain text), on one line;
 *  nothing for an empty reply or a web page. */
async function replyDetail(r: Response): Promise<{ detail: string; errorId?: string; code?: string }> {
  let text: string
  try {
    text = await r.text()
  } catch {
    return { detail: '' }
  }
  try {
    const j: unknown = JSON.parse(text)
    const err = (j as { error?: unknown } | null)?.error
    const id = (j as { error_id?: unknown } | null)?.error_id
    const code = (j as { code?: unknown } | null)?.code
    if (typeof err === 'string' && err.trim()) {
      return {
        detail: err.trim(),
        ...(typeof id === 'string' && id ? { errorId: id } : {}),
        ...(typeof code === 'string' && /^[a-z_]{1,40}$/.test(code) ? { code } : {}),
      }
    }
    return { detail: '' }
  } catch { /* not JSON */ }
  const line = withoutControls(text).replace(/\s+/g, ' ').trim()
  if (line === '' || /^<(!doctype|html)/i.test(line)) return { detail: '' }
  return { detail: line.length > 200 ? `${line.slice(0, 199)}…` : line }
}

/** The error a refused read (raw, download) throws. 410: listed but gone. 404 (an older server's
 *  answer for the same, or a file deleted meanwhile) reads the same to a person. */
async function readFailure(r: Response, other: string): Promise<Error> {
  if (r.status === 410) return new ArtifactGoneError()
  if (r.status === 404) return new Error(FILE_GONE_MESSAGE)
  const { detail, errorId } = await replyDetail(r)
  return new FileRequestError(detail || other, r.status, errorId)
}

/** A read of the session's folder the server refused: the status is what the UI branches on. The
 *  server's own text is kept for matching, never for showing. */
export class WorkspaceFileError extends Error {
  readonly status: number
  readonly errorId?: string
  readonly serverText: string
  /** The server's machine-readable reason (busy, daemon_timeout, changed, not_running, …): what
   *  retries branch on (model/imageQueue.ts). Absent from an older server. */
  readonly code?: string
  constructor(status: number, serverText: string, errorId?: string, code?: string) {
    super(`workspace file refused (HTTP ${status})`)
    this.name = 'WorkspaceFileError'
    this.status = status
    this.serverText = serverText
    if (errorId) this.errorId = errorId
    if (code) this.code = code
  }
}

function fromBase64(b64: string): Uint8Array<ArrayBuffer> {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

/** The file name in a `Content-Disposition: attachment; filename*=UTF-8''...` header, with any
 *  folder dropped; null when there is none or it does not decode. */
export function filenameFromDisposition(header: string | null): string | null {
  const m = header ? /filename\*=UTF-8''([^;]+)/i.exec(header) : null
  if (!m) return null
  try {
    const name = decodeURIComponent(m[1].trim()).replace(/\\/g, '/').split('/').pop() ?? ''
    return name.trim() === '' ? null : name
  } catch {
    return null
  }
}

/** Hands a blob to the browser to save: a temporary anchor on an object URL, revoked right after. */
export function saveBlob(blob: Blob, name: string): void {
  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.rel = 'noopener'
    a.style.display = 'none'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
}

/** Why an upload was refused, in words fit to show, with the server's error id. */
async function uploadRefusal(r: Response): Promise<FileRequestError> {
  const { detail, errorId, code } = await replyDetail(r)
  const say = (fallback: string) => new FileRequestError(detail || fallback, r.status, errorId, code)
  if (r.status === 413) return say('That file is larger than the server takes.')
  if (r.status === 409) return say('This session can’t take more files.')
  if (r.status === 404) return new FileRequestError('This session can’t be found.', r.status, errorId)
  return say(`Upload failed (HTTP ${r.status}).`)
}

/** What an upload that got no answer at all says: the connection went before the server replied.
 *  A server or proxy that refuses a large body without reading it ends the upload this way. */
export const UPLOAD_CUT_OFF_MESSAGE =
  'The upload was cut off before the server answered: the connection dropped, or something between you and the server refused the file (often its size).'

/** Sends a file like fetch would (same headers) but reports upload progress, which fetch cannot.
 *  Resolves to an ordinary Response. */
export function uploadWithProgress(
  url: string,
  file: Blob,
  headers: Record<string, string>,
  onProgress?: (loaded: number, total: number) => void,
  signal?: AbortSignal,
  credentials?: RequestCredentials,
): Promise<Response> {
  return new Promise<Response>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', url)
    if (credentials === 'include') xhr.withCredentials = true
    for (const [k, v] of Object.entries(headers)) xhr.setRequestHeader(k, v)
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress?.(e.loaded, e.total)
    }
    const onAbort = () => xhr.abort()
    xhr.onload = () => {
      signal?.removeEventListener('abort', onAbort)
      resolve(new Response(xhr.status === 204 ? null : xhr.responseText, {
        status: xhr.status,
        headers: { 'Content-Type': xhr.getResponseHeader('Content-Type') ?? 'application/json' },
      }))
    }
    xhr.onerror = () => {
      signal?.removeEventListener('abort', onAbort)
      reject(new TypeError(UPLOAD_CUT_OFF_MESSAGE))
    }
    xhr.onabort = () => {
      signal?.removeEventListener('abort', onAbort)
      reject(new DOMException('The upload was canceled.', 'AbortError'))
    }
    if (signal?.aborted) {
      xhr.abort()
      return
    }
    signal?.addEventListener('abort', onAbort)
    xhr.send(file)
  })
}

/** A reply's body when it is at most `max` bytes, else null, the download stopped as soon as that is
 *  known: by Content-Length, else by the stream passing `max`. Without either (no length, no
 *  stream) the size cannot be told without reading it all: null. */
export async function readCapped(r: Response, max: number, abort?: AbortController): Promise<Blob | null> {
  const stop = () => { abort?.abort(); void r.body?.cancel().catch(() => {}) }
  const length = Number(r.headers.get('Content-Length') ?? '')
  const known = r.headers.has('Content-Length') && Number.isFinite(length)
  if (known && length > max) { stop(); return null }
  const type = r.headers.get('Content-Type') ?? ''
  if (!r.body) {
    if (!known) return null
    return r.blob()
  }
  const reader = r.body.getReader()
  const parts: Uint8Array[] = []
  let total = 0
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    total += value.byteLength
    if (total > max) {
      await reader.cancel().catch(() => {})
      abort?.abort()
      return null
    }
    parts.push(value)
  }
  return new Blob(parts as BlobPart[], type ? { type } : undefined)
}

export function createFiles(opts: FilesOptions): TransportFiles {
  const fetchImpl = opts.fetch ?? globalThis.fetch.bind(globalThis)
  const artifacts = (sessionId: string) => `${opts.base(sessionId)}/artifacts`

  const request = async (url: string, init?: RequestInit): Promise<Response> => {
    const headers = { ...(await opts.headers?.()), ...(init?.headers as Record<string, string> | undefined) }
    const res = await fetchImpl(url, { ...init, headers, credentials: opts.credentials })
    opts.check?.(res)
    return res
  }

  const workspaceFile = async (sessionId: string, path: string, signal?: AbortSignal): Promise<WorkspaceFile> => {
    const r = await request(`${opts.base(sessionId)}/workspace-file?path=${enc(path)}`, { signal })
    if (!r.ok) {
      const { detail, errorId, code } = await replyDetail(r)
      throw new WorkspaceFileError(r.status, detail, errorId, code)
    }
    const j = (await r.json().catch(() => null)) as Partial<WorkspaceFile> & { content_b64?: unknown } | null
    if (!j || typeof j.content_b64 !== 'string' || typeof j.view !== 'string') throw new WorkspaceFileError(502, '')
    const bytes = fromBase64(j.content_b64)
    return {
      path: typeof j.path === 'string' ? j.path : path,
      size: bytes.length,
      view: j.view,
      content_type: typeof j.content_type === 'string' ? j.content_type : '',
      mtime: typeof j.mtime === 'string' ? j.mtime : '',
      // An image's bytes keep their type, so an <img> draws an SVG (it does not sniff one).
      blob: new Blob([bytes], typeof j.content_type === 'string' && imageBlobType(j.content_type) ? { type: imageBlobType(j.content_type) } : undefined),
    }
  }

  const commitUrl = (sessionId: string, sha: string) => `${opts.base(sessionId)}/commits/${enc(sha)}`

  const qualification = (member?: WorkspaceGitRef) => member ? `?member_id=${enc(member.member_id)}&launch_generation=${enc(member.launch_generation)}&generation=${member.generation}` : ''
  const sameMember = (a: Partial<WorkspaceGitRef> | null, b: WorkspaceGitRef) => a?.member_id === b.member_id && a.launch_generation === b.launch_generation && a.generation === b.generation

  /** The commit's summary; null for every refusal (no access, no workspace, unknown sha). */
  const commit = async (sessionId: string, sha: string, signal?: AbortSignal, member?: WorkspaceGitRef): Promise<CommitDetails | null> => {
    const s = sha.toLowerCase()
    if (!COMMIT_SHA.test(s)) return null
    const r = await request(commitUrl(sessionId, s) + qualification(member), { signal })
    if (!r.ok) return null
    const c = commitDetails(await r.json().catch(() => null))
    return c && c.sha.startsWith(s) && (!member || sameMember(c, member)) ? c : null
  }

  /** The commit's diff, read no further than COMMIT_DIFF_MAX. */
  const commitDiff = async (sessionId: string, sha: string, signal?: AbortSignal, member?: WorkspaceGitRef): Promise<CommitDiff> => {
    const s = sha.toLowerCase()
    if (!COMMIT_SHA.test(s)) return null
    const abort = new AbortController()
    const stop = () => abort.abort()
    signal?.addEventListener('abort', stop)
    try {
      const r = await request(`${commitUrl(sessionId, s)}/diff${qualification(member)}`, { signal: abort.signal })
      if (r.status === 413) return 'large'
      if (!r.ok) return null
      if (member && (r.headers.get('X-Blerg-Member-ID') !== member.member_id || r.headers.get('X-Blerg-Launch-Generation') !== member.launch_generation || Number(r.headers.get('X-Blerg-Generation')) !== member.generation)) return null
      const blob = await readCapped(r, COMMIT_DIFF_MAX, abort)
      return blob ? { text: await blob.text(), ...(member ? { member } : {}) } : 'large'
    } finally {
      signal?.removeEventListener('abort', stop)
    }
  }

  return {
    ...(opts.queueScope ? {queueScope: opts.queueScope} : {}),
    ...(opts.workspace ? { workspaceFile } : {}),
    ...(opts.commits ? { commit, commitDiff } : {}),
    async space(sessionId) {
      const r = await request(artifacts(sessionId))
      if (!r.ok) {
        const {detail, errorId} = await replyDetail(r)
        throw new FileRequestError(detail || 'Couldn’t read the session’s space.', r.status, errorId)
      }
      const body = await r.json()
      const all = body.can_delete === true
      // An older runner decides per session only: its files take the session's answer.
      const rows = (Array.isArray(body.artifacts) ? body.artifacts : []).map((a: ArtifactInfo) => typeof a?.can_delete === 'boolean' ? a : {...a, can_delete: all})
      return {artifacts: rows, limits: body.limits ?? {}, can_delete: all, can_increase: body.can_increase === true}
    },
    async increaseStorage(sessionId) {
      const r = await request(`${opts.base(sessionId)}/storage`, {method: 'POST'})
      if (r.ok) return
      const {detail, errorId, code} = await replyDetail(r)
      const fallback = r.status === 403 ? 'Only an administrator can increase this session’s storage.' : r.status === 409 ? 'This session already has the most storage this install allows.' : r.status >= 500 || r.status === 0 ? 'Couldn’t reach the server. Try again.' : 'Couldn’t increase the storage. Try again.'
      throw new FileRequestError(detail || fallback, r.status, errorId, code)
    },
    /** The session's artifacts, newest first. Throws when the list cannot be read. */
    async list(sessionId) {
      const r = await request(artifacts(sessionId))
      if (!r.ok) throw new Error(`HTTP ${r.status}`)
      const body: unknown = await r.json()
      const list = (body as { artifacts?: unknown } | null)?.artifacts
      if (!Array.isArray(list)) return []
      return list.filter((a): a is ArtifactInfo => !!a && typeof a === 'object' && typeof (a as ArtifactInfo).id === 'string')
    },

    async raw(sessionId, id) {
      const r = await request(`${artifacts(sessionId)}/${enc(id)}/raw`)
      if (!r.ok) throw await readFailure(r, `Couldn’t load this file (HTTP ${r.status}).`)
      return r.blob()
    },

    async rawCapped(sessionId, id, max) {
      const abort = new AbortController()
      const r = await request(`${artifacts(sessionId)}/${enc(id)}/raw`, { signal: abort.signal })
      if (!r.ok) throw await readFailure(r, `Couldn’t load this file (HTTP ${r.status}).`)
      return readCapped(r, max, abort)
    },

    /** Saves the file: fetched with the credentials (a plain link could not carry a token), then
     *  handed to the browser. It is saved under the name the server sent (Content-Disposition),
     *  else its id. */
    async download(sessionId, id) {
      const r = await request(`${artifacts(sessionId)}/${enc(id)}/download`)
      if (!r.ok) throw await readFailure(r, `Download failed (HTTP ${r.status}).`)
      const saveAs = filenameFromDisposition(r.headers.get('Content-Disposition')) ?? id
      saveBlob(await r.blob(), saveAs)
    },

    async remove(sessionId, id) {
      const r = await request(`${artifacts(sessionId)}/${enc(id)}`, { method: 'DELETE' })
      if (r.ok || r.status === 404) return
      const { detail, errorId } = await replyDetail(r)
      const fallback = r.status === 403
        ? 'You can delete only the files you attached. The session’s owner or an administrator can delete the others.'
        : r.status >= 500 ? 'The server couldn’t delete the file. Try again.' : 'Couldn’t delete the file. Try again.'
      throw new FileRequestError(detail || fallback, r.status, errorId)
    },

    /** Uploads one file to the session as the person's attachment. Throws an Error whose message
     *  is fit to show (a FileRequestError, with the server's error id, when the server refused). */
    async upload(sessionId, file, onProgress, signal) {
      const headers = {
        ...(await (opts.headers ?? opts.uploadHeaders)?.()),
        'Content-Type': 'application/octet-stream',
        'X-Artifact-Name': enc(file.name),
      }
      const r = await uploadWithProgress(`${opts.base(sessionId)}/uploads`, file, headers, onProgress, signal, opts.credentials)
      opts.check?.(r)
      opts.checkUpload?.(r)
      if (!r.ok) throw await uploadRefusal(r)
      const j = (await r.json()) as Partial<UploadedFile>
      if (typeof j.id !== 'string') throw new Error('Unexpected reply from the server.')
      return { id: j.id, name: typeof j.name === 'string' ? j.name : file.name, size: typeof j.size === 'number' ? j.size : file.size }
    },
  }
}
