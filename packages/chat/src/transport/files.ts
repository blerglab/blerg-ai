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

/** Why an upload was refused, in words fit to show. */
async function uploadRefusal(r: Response): Promise<string> {
  let detail = ''
  try {
    const j: unknown = await r.json()
    detail = ((j as { error?: unknown } | null)?.error as string) ?? ''
  } catch { /* not JSON */ }
  if (r.status === 413) return detail || 'That file is larger than 25 MiB.'
  if (r.status === 409) return detail || 'This session can’t take more files.'
  if (r.status === 404) return 'This session can’t be found.'
  return detail || `Upload failed (HTTP ${r.status}).`
}

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
      reject(new TypeError('Network error'))
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
      if (!r.ok) throw new Error(r.status === 404 ? 'This file is no longer available.' : `HTTP ${r.status}`)
      return r.blob()
    },

    /** Saves the file: fetched with the credentials (a plain link could not carry a token), then
     *  handed to the browser. It is saved under the name the server sent (Content-Disposition),
     *  else its id. */
    async download(sessionId, id) {
      const r = await request(`${artifacts(sessionId)}/${enc(id)}/download`)
      if (!r.ok) throw new Error(r.status === 404 ? 'This file is no longer available.' : `Download failed (HTTP ${r.status}).`)
      const saveAs = filenameFromDisposition(r.headers.get('Content-Disposition')) ?? id
      saveBlob(await r.blob(), saveAs)
    },

    async remove(sessionId, id) {
      const r = await request(`${artifacts(sessionId)}/${enc(id)}`, { method: 'DELETE' })
      if (!r.ok && r.status !== 404) throw new Error(`HTTP ${r.status}`)
    },

    /** Uploads one file to the session as the person's attachment. Throws an Error whose message
     *  is fit to show. */
    async upload(sessionId, file, onProgress, signal) {
      const headers = {
        ...(await (opts.headers ?? opts.uploadHeaders)?.()),
        'Content-Type': 'application/octet-stream',
        'X-Artifact-Name': enc(file.name),
      }
      const r = await uploadWithProgress(`${opts.base(sessionId)}/uploads`, file, headers, onProgress, signal, opts.credentials)
      opts.check?.(r)
      opts.checkUpload?.(r)
      if (!r.ok) throw new Error(await uploadRefusal(r))
      const j = (await r.json()) as Partial<UploadedFile>
      if (typeof j.id !== 'string') throw new Error('Unexpected reply from the server.')
      return { id: j.id, name: typeof j.name === 'string' ? j.name : file.name, size: typeof j.size === 'number' ? j.size : file.size }
    },
  }
}
