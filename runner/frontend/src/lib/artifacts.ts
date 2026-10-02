// Session artifacts: files an agent handed the user (`blerg-runner publish`). The API calls, and
// the small pure helpers the viewer needs (size text, CSV, JSON, the HTML sandbox's CSP).
import { apiFetch } from '../apiFetch'
import type { ArtifactPayload } from '../types'

export type ArtifactInfo = ArtifactPayload & { created_at?: string }

const base = (sessionId: string) => `/api/sessions/${encodeURIComponent(sessionId)}/artifacts`
export const artifactRawUrl = (sessionId: string, id: string) => `${base(sessionId)}/${encodeURIComponent(id)}/raw`
export const artifactDownloadUrl = (sessionId: string, id: string) => `${base(sessionId)}/${encodeURIComponent(id)}/download`

export function formatSize(n: number): string {
  if (!Number.isFinite(n) || n < 0) return ''
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`
  return `${(n / (1024 * 1024)).toFixed(1)} MB`
}

// ─── API ──────────────────────────────────────────────────────────────────────

/** The session's artifacts, newest first. Throws when the list cannot be read. */
export async function listArtifacts(sessionId: string): Promise<ArtifactInfo[]> {
  const r = await apiFetch(base(sessionId))
  if (!r.ok) throw new Error(`HTTP ${r.status}`)
  const body: unknown = await r.json()
  const list = (body as { artifacts?: unknown } | null)?.artifacts
  if (!Array.isArray(list)) return []
  return list.filter((a): a is ArtifactInfo => !!a && typeof a === 'object' && typeof (a as ArtifactInfo).id === 'string')
}

export async function deleteArtifact(sessionId: string, id: string): Promise<void> {
  const r = await apiFetch(`${base(sessionId)}/${encodeURIComponent(id)}`, { method: 'DELETE' })
  if (!r.ok && r.status !== 404) throw new Error(`HTTP ${r.status}`)
}

/** Saves the file: fetched with the bearer token (a plain link could not carry it), then handed
 *  to the browser through a temporary anchor on an object URL that is revoked right after. It is
 *  saved under the name the server sent (Content-Disposition), else the one downloadFileName gives. */
export async function downloadArtifact(sessionId: string, art: Pick<ArtifactInfo, 'id' | 'name' | 'version' | 'latest_version'>): Promise<void> {
  const r = await apiFetch(artifactDownloadUrl(sessionId, art.id))
  if (!r.ok) throw new Error(r.status === 404 ? 'This file is no longer available.' : `Download failed (HTTP ${r.status}).`)
  const saveAs = filenameFromDisposition(r.headers.get('Content-Disposition')) ?? downloadFileName(art)
  const url = URL.createObjectURL(await r.blob())
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = saveAs
    a.rel = 'noopener'
    a.style.display = 'none'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
}

// ─── versions ─────────────────────────────────────────────────────────────────

/** The name a file is saved under, the way the server names its download: the plain name for the
 *  newest version (or a file with no other), <stem>-v<N><ext> for an older one (a name without an
 *  extension just gets -v<N>). */
export function downloadFileName(a: Pick<ArtifactInfo, 'name' | 'version' | 'latest_version'>): string {
  const { name, version, latest_version: latest } = a
  if (!version || !latest || version >= latest) return name
  const dot = name.lastIndexOf('.')
  if (dot <= 0) return `${name}-v${version}` // no extension (a leading dot is not one)
  return `${name.slice(0, dot)}-v${version}${name.slice(dot)}`
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

/** One file of the session, with every version of it: `latest` is the highest version, `earlier`
 *  the rest, newest first. */
export interface ArtifactGroup {
  key: string
  name: string
  origin: 'agent' | 'user'
  latest: ArtifactInfo
  earlier: ArtifactInfo[]
}

/** Groups a session's list (newest first) by (origin, name), in the order the files first appear.
 *  A file the server gave no version (an older server) stands alone, as it always did. */
export function groupArtifacts(items: ArtifactInfo[]): ArtifactGroup[] {
  const groups = new Map<string, ArtifactInfo[]>()
  for (const a of items) {
    const origin = a.origin === 'user' ? 'user' : 'agent'
    const key = typeof a.version === 'number' ? `${origin}\0${a.name}` : `${origin}\0${a.name}\0${a.id}`
    const list = groups.get(key)
    if (list) list.push(a)
    else groups.set(key, [a])
  }
  return [...groups].map(([key, list]) => {
    const [latest, ...earlier] = [...list].sort((x, y) => (y.version ?? 0) - (x.version ?? 0))
    return { key, name: latest.name, origin: latest.origin === 'user' ? 'user' : 'agent', latest, earlier }
  })
}

/** How many files (not versions) the list holds. */
export function fileCount(items: ArtifactInfo[]): number {
  return groupArtifacts(items).length
}

/** The newest version of the file `a` is a version of, from the session's list; undefined when
 *  the list does not hold that file. */
export function findLatest(items: ArtifactInfo[], a: Pick<ArtifactInfo, 'name' | 'origin'>): ArtifactInfo | undefined {
  const origin = a.origin === 'user' ? 'user' : 'agent'
  return groupArtifacts(items).find(g => g.name === a.name && g.origin === origin)?.latest
}

// ─── CSV (RFC 4180) ───────────────────────────────────────────────────────────

export interface CsvResult {
  rows: string[][]
  /** There were more rows than maxRows; the rest were not parsed. */
  moreRows: boolean
  /** Some row had more than maxCols fields; the extras were dropped. */
  moreCols: boolean
}

/** Parses delimited text: quoted fields may hold the delimiter, newlines and "" (an escaped
 *  quote); CRLF, LF and CR all end a row; a BOM is ignored; a newline at the very end does not
 *  make an empty row. Stops after maxRows rows, and keeps at most maxCols fields per row. */
export function parseCsv(text: string, delimiter = ',', maxRows = Infinity, maxCols = Infinity): CsvResult {
  if (text.charCodeAt(0) === 0xfeff) text = text.slice(1)
  const rows: string[][] = []
  let row: string[] = []
  let field = ''
  let quoted = false // inside a quoted field
  let started = false // something belongs to the current row (so a lone "" still makes one)
  let moreRows = false
  let moreCols = false

  const endField = () => {
    if (row.length < maxCols) row.push(field)
    else moreCols = true
    field = ''
  }
  // Returns false when the row limit is hit and parsing should stop.
  const endRow = () => {
    endField()
    if (rows.length >= maxRows) {
      moreRows = true
      return false
    }
    rows.push(row)
    row = []
    started = false
    return true
  }

  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (quoted) {
      if (c === '"') {
        if (text[i + 1] === '"') { field += '"'; i++ } else quoted = false
      } else field += c
      continue
    }
    if (c === '"' && field === '') { quoted = true; started = true; continue }
    if (c === delimiter) { started = true; endField(); continue }
    if (c === '\n' || c === '\r') {
      if (c === '\r' && text[i + 1] === '\n') i++
      if (!endRow()) return { rows, moreRows, moreCols }
      continue
    }
    started = true
    field += c
  }
  if (started || field !== '' || row.length > 0) {
    if (!endRow()) return { rows, moreRows, moreCols }
  }
  return { rows, moreRows, moreCols }
}

// ─── JSON ─────────────────────────────────────────────────────────────────────

/** Pretty-printed JSON, or the text as it is when it does not parse. */
export function prettyJson(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2)
  } catch {
    return text
  }
}

// ─── HTML sandbox ─────────────────────────────────────────────────────────────

/** What a published HTML page may do: run its own inline script and style and show data: images,
 *  fonts and media, and nothing else. No network at all (connect-src falls back to 'none'), and it
 *  cannot submit a form anywhere, retarget its links with <base>, or embed another page. */
export const HTML_ARTIFACT_CSP =
  "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; font-src data:; media-src data: blob:; form-action 'none'; base-uri 'none'; frame-src 'none'"

export const HTML_ARTIFACT_CSP_META = `<meta http-equiv="Content-Security-Policy" content="${HTML_ARTIFACT_CSP}">`

/** The page's source with the CSP meta ahead of everything the page can run. A leading
 *  <!doctype html> stays first (anything before it would put the page in quirks mode); it holds
 *  nothing executable. */
export function withCsp(html: string): string {
  const doctype = /^\s*<!doctype html[^>]*>/i.exec(html)
  if (doctype) return doctype[0] + HTML_ARTIFACT_CSP_META + html.slice(doctype[0].length)
  return HTML_ARTIFACT_CSP_META + html
}
