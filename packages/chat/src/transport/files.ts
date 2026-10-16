// The session-file operations both adapters share: the routes have one shape on the runner's
// browser API and on the proxy contract (list, raw, download, delete, upload), only the prefix and
// the authentication differ. Ported from the runner's lib/artifacts.ts, lib/attachments.ts and
// apiFetch.ts; the pure helpers (grouping, versions, size text) live in the model, not here.
import type { ArtifactInfo, TransportFiles, UploadedFile } from './types'

export interface FilesOptions {
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
  constructor(message: string, status: number, errorId?: string) {
    super(message)
    this.name = 'FileRequestError'
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
async function replyDetail(r: Response): Promise<{ detail: string; errorId?: string }> {
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
    if (typeof err === 'string' && err.trim()) {
      return typeof id === 'string' && id ? { detail: err.trim(), errorId: id } : { detail: err.trim() }
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
  const { detail, errorId } = await replyDetail(r)
  const say = (fallback: string) => new FileRequestError(detail || fallback, r.status, errorId)
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
      reject(new DOMException('The upload was cancelled.', 'AbortError'))
    }
    if (signal?.aborted) {
      xhr.abort()
      return
    }
    signal?.addEventListener('abort', onAbort)
    xhr.send(file)
  })
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

  return {
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
      if (!r.ok && r.status !== 404) throw new Error(`HTTP ${r.status}`)
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
