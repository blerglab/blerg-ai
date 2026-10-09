// Inline previews in the transcript: which files get one, and their bytes. An image is drawn
// in its card (fetched through the transport into a blob URL, like the viewer, and only once per
// session and file); a video or audio file gets a play tile and is not fetched until it is
// opened, since a first frame would cost the whole file; an HTML page is drawn small, in the same
// sandbox the viewer uses (components/ArtifactCard.tsx), and opens in the viewer to be used.
import { TEXT_VIEW_MAX, blobTypeFor } from './artifactViews'

/** An image over this size keeps the plain card: the transcript is not a download manager. */
export const INLINE_IMAGE_MAX = 10 * 1024 * 1024

/** An HTML page over this size keeps the plain card (the viewer will not render it either). */
export const INLINE_HTML_MAX = TEXT_VIEW_MAX

/** The page an inline HTML preview is laid out on before it is scaled down to its card: a
 *  desktop-width page, so what is shown small is what the viewer shows large. */
export const HTML_PREVIEW_PAGE = { width: 1024, height: 640 }

export type PreviewKind = 'image' | 'media' | 'html'

export function previewKind(view: string, size: number): PreviewKind | null {
  if (view === 'image') return size <= INLINE_IMAGE_MAX ? 'image' : null
  if (view === 'video' || view === 'audio') return 'media'
  if (view === 'html') return size <= INLINE_HTML_MAX ? 'html' : null
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
      return URL.createObjectURL(new Blob([bytes], { type: blobTypeFor(artifact.view, artifact.content_type) }))
    })()
    p.catch(() => cache.delete(key))
    cache.set(key, p)
  }
  return p
}

const textCache = new Map<string, Promise<string>>()

/** The source of an HTML page, fetched once per session and file through `raw`. A failed fetch
 *  is forgotten so a retry can work. */
export function loadPreviewText(sessionId: string, artifactId: string, raw: (id: string) => Promise<Blob>): Promise<string> {
  const key = `${sessionId}/${artifactId}`
  let p = textCache.get(key)
  if (!p) {
    p = raw(artifactId).then(b => b.text())
    p.catch(() => textCache.delete(key))
    textCache.set(key, p)
  }
  return p
}

/** Forgets every cached preview (tests, or a page that leaves the session). */
export function clearPreviewCache(): void {
  for (const p of cache.values()) p.then(u => URL.revokeObjectURL(u)).catch(() => {})
  cache.clear()
  textCache.clear()
}
