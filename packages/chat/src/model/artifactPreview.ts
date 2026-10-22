// Inline previews in the transcript: which files get one, and their bytes. An image is drawn
// in its card (fetched through the transport into a blob URL, like the viewer, and only once per
// session and file); a video or audio file gets a play tile and is not fetched until it is
// opened, since a first frame would cost the whole file; an HTML page is drawn small, in the same
// sandbox the viewer uses (components/ArtifactCard.tsx), and opens in the viewer to be used.
// Markdown, text and code, JSON and tables show a short excerpt; a PDF shows its first page.
// All of these are gated by the size the list reports, so nothing big is ever fetched for a
// preview, and only the small result (an excerpt, a thumbnail) is kept, in a bounded cache.
import { TEXT_VIEW_MAX, mediaUrl, releaseMediaUrl } from './artifactViews'
import { excerptOf, type Excerpt } from './previewExcerpt'
import { parsePatch, patchSummary, type PatchFile, type PatchSummary } from './patch'
import { renderPdfThumb } from '../pdf/thumb'

/** An image over this size keeps the plain card: the transcript is not a download manager. */
export const INLINE_IMAGE_MAX = 10 * 1024 * 1024

/** An HTML page over this size keeps the plain card (the viewer will not render it either). */
export const INLINE_HTML_MAX = TEXT_VIEW_MAX

/** The page an inline HTML preview is laid out on before it is scaled down to its card: a
 *  desktop-width page, so what is shown small is what the viewer shows large. */
export const HTML_PREVIEW_PAGE = { width: 1024, height: 640 }

/** Text-like files preview only up to this size (they are read whole to cut the excerpt). */
export const INLINE_TEXT_MAX = 256 * 1024

/** A PDF previews only up to this size (its first page is drawn, then the document is released). */
export const INLINE_PDF_MAX = 5 * 1024 * 1024

export type PreviewKind = 'image' | 'media' | 'html' | 'markdown' | 'text' | 'csv' | 'pdf' | 'diff'

/** The kinds drawn as an excerpt of the file's text. */
export const EXCERPT_KINDS: ReadonlySet<PreviewKind> = new Set(['markdown', 'text', 'csv'])

export function previewKind(view: string, size: number): PreviewKind | null {
  if (view === 'image') return size <= INLINE_IMAGE_MAX ? 'image' : null
  if (view === 'video' || view === 'audio') return 'media'
  if (view === 'html') return size <= INLINE_HTML_MAX ? 'html' : null
  if (view === 'markdown') return size <= INLINE_TEXT_MAX ? 'markdown' : null
  if (view === 'text' || view === 'json') return size <= INLINE_TEXT_MAX ? 'text' : null
  if (view === 'csv') return size <= INLINE_TEXT_MAX ? 'csv' : null
  if (view === 'pdf') return size <= INLINE_PDF_MAX ? 'pdf' : null
  // A larger patch keeps the plain card (its name and size): it is not read for a count.
  if (view === 'diff') return size <= INLINE_TEXT_MAX ? 'diff' : null
  return null
}

const cache = new Map<string, Promise<string>>()

/** The blob URL of a file's bytes, fetched once per session and file through `raw` (the
 *  transport's authenticated read) and kept for the page's life (bounded by INLINE_IMAGE_MAX per
 *  image). The blob is retyped by the file's view: the server's content type is not taken for one
 *  the browser would render. A failed fetch is forgotten so a retry can work. */
export function loadPreviewUrl(
  sessionId: string,
  artifact: { id: string; view: string; content_type: string },
  raw: (id: string) => Promise<Blob>,
): Promise<string> {
  const key = `${sessionId}/${artifact.id}`
  let p = cache.get(key)
  if (!p) {
    p = (async () => {
      const bytes = await raw(artifact.id)
      return mediaUrl(bytes, artifact.view, artifact.content_type) // an SVG as a data: URL, never blob:
    })()
    p.catch(() => cache.delete(key))
    cache.set(key, p)
  }
  return p
}

const textCache = new Map<string, Promise<string>>()

/** The source of an HTML page, fetched once per session and file through `raw` (bounded like the
 *  other small caches). A failed fetch is forgotten so a retry can work. */
export function loadPreviewText(sessionId: string, artifactId: string, raw: (id: string) => Promise<Blob>): Promise<string> {
  return remember(textCache, `${sessionId}/${artifactId}`, async () => (await raw(artifactId)).text())
}

// The small results of the text and PDF previews, at most this many at a time (oldest out first).
const SMALL_MAX = 50

function remember<T>(map: Map<string, Promise<T>>, key: string, make: () => Promise<T>): Promise<T> {
  const hit = map.get(key)
  if (hit) {
    map.delete(key) // most recently used last
    map.set(key, hit)
    return hit
  }
  const p = make()
  p.catch(() => map.delete(key))
  map.set(key, p)
  if (map.size > SMALL_MAX) map.delete(map.keys().next().value as string)
  return p
}

const excerpts = new Map<string, Promise<Excerpt>>()

/** The excerpt of a markdown, text, JSON or CSV file, fetched once per session and file. The file
 *  is read whole (it is at most INLINE_TEXT_MAX, by previewKind) but only the excerpt is kept. */
export function loadPreviewExcerpt(
  sessionId: string,
  artifact: { id: string; name: string; view: string; content_type: string },
  raw: (id: string) => Promise<Blob>,
): Promise<Excerpt> {
  return remember(excerpts, `${sessionId}/${artifact.id}`, async () => {
    const text = await (await raw(artifact.id)).text()
    return excerptOf(artifact.view, artifact.name, artifact.content_type, text)
  })
}

/** A patch of one file with at most this many changed lines (adds + dels) is drawn in its card. */
export const INLINE_DIFF_LINES = 20

export interface DiffPreviewData { summary: PatchSummary; inline: PatchFile | null }

const diffs = new Map<string, Promise<DiffPreviewData>>()

/** What a diff's card says (files, +/−, the first names), read once per session and file. The
 *  patch is read whole (at most INLINE_TEXT_MAX, by previewKind) but only the summary is kept. */
export function loadDiffSummary(sessionId: string, artifactId: string, raw: (id: string) => Promise<Blob>): Promise<PatchSummary> {
  return loadDiffPreview(sessionId, artifactId, raw).then(d => d.summary)
}

/** The card's summary and, for a small patch of ONE file (at most INLINE_DIFF_LINES changed lines),
 *  that file parsed, to draw its hunks in the card. The patch is parsed once. */
export function loadDiffPreview(sessionId: string, artifactId: string, raw: (id: string) => Promise<Blob>): Promise<DiffPreviewData> {
  return remember(diffs, `${sessionId}/${artifactId}`, async () => {
    const parsed = parsePatch(await (await raw(artifactId)).text())
    const summary = patchSummary(parsed)
    const one = parsed.files.length === 1 ? parsed.files[0] : null
    const inline = one && !one.binary && one.hunks.length > 0 && one.adds + one.dels > 0 && one.adds + one.dels <= INLINE_DIFF_LINES ? one : null
    return { summary, inline }
  })
}

const thumbs = new Map<string, Promise<string>>()

/** The first page of a PDF as a small picture (a data URL), made once per session and file. */
export function loadPdfThumb(sessionId: string, artifactId: string, raw: (id: string) => Promise<Blob>): Promise<string> {
  return remember(thumbs, `${sessionId}/${artifactId}`, async () => renderPdfThumb(await raw(artifactId)))
}

/** Forgets every cached preview (tests, or a page that leaves the session). */
export function clearPreviewCache(): void {
  for (const p of cache.values()) p.then(u => releaseMediaUrl(u)).catch(() => {})
  cache.clear()
  textCache.clear()
  excerpts.clear()
  diffs.clear()
  thumbs.clear()
}
