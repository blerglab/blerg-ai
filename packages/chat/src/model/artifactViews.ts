// How the app shows a published file (an "artifact"). ONE table, keyed by the `view` string the
// server assigns (runner/internal/server/artifact_types.go): the server decides a file's view
// from its name and bytes; this says what the browser does with each. A view this table does not
// know is download-only, so a newer server never breaks an older page.
//
// Nothing here renders anything; ArtifactViewer does. Every renderer takes the file's bytes from
// the authenticated /raw route, never from a URL the page could be talked into loading.

export interface ArtifactViewSpec {
  /** Shown in the viewer header. */
  label: string
  /** True when the file is fetched and shown as text (capped; see TEXT_PREVIEW_CAP). */
  textual: boolean
  /** Content types a blob for this view may carry. Anything else becomes octet-stream. */
  blobTypes?: RegExp
}

export const ARTIFACT_VIEWS: Record<string, ArtifactViewSpec> = {
  markdown: { label: 'Markdown', textual: true },
  text: { label: 'Text', textual: true },
  diff: { label: 'Diff', textual: true },
  json: { label: 'JSON', textual: true },
  csv: { label: 'Table', textual: true },
  html: { label: 'HTML', textual: true },
  // SVG is an image here and is ONLY ever shown through <img>, where script never runs, and from a
  // data: URL (mediaUrl), never a blob: one: a blob: URL has the app's origin, so an SVG opened from
  // it in a new tab would run its script as the app.
  image: { label: 'Image', textual: false, blobTypes: /^image\/(png|jpeg|gif|webp)$/ },
  pdf: { label: 'PDF', textual: false, blobTypes: /^application\/pdf$/ },
  audio: { label: 'Audio', textual: false, blobTypes: /^audio\/(mpeg|wav|ogg|mp4)$/ },
  video: { label: 'Video', textual: false, blobTypes: /^video\/(mp4|webm)$/ },
}

/** Text previews show this many characters before "Show all". */
export const TEXT_PREVIEW_CAP = 200 * 1024
/** A textual file over this size is download-only. */
export const TEXT_VIEW_MAX = 2 * 1024 * 1024
export const CSV_MAX_ROWS = 500
export const CSV_MAX_COLS = 50

/** Whether the app can show a file of this view (false for "none" and anything unknown). */
export function isViewable(view: string): boolean {
  return Object.prototype.hasOwnProperty.call(ARTIFACT_VIEWS, view)
}

/** The type a blob of this file is created with. Only allow-listed types survive, and a PDF is
 *  ALWAYS application/pdf: the server's word is not taken for a type the browser would render.
 *  Never image/svg+xml: a same-origin blob: URL of an SVG opened in a tab runs its script as the
 *  app. Draw an SVG through mediaUrl, which gives it a data: URL instead. */
export function blobTypeFor(view: string, contentType: string): string {
  if (view === 'pdf') return 'application/pdf'
  const spec = Object.prototype.hasOwnProperty.call(ARTIFACT_VIEWS, view) ? ARTIFACT_VIEWS[view] : undefined
  const base = contentType.split(';')[0].trim().toLowerCase()
  return spec?.blobTypes?.test(base) ? base : 'application/octet-stream'
}

/** Whether a file of this view and size is too big to preview (textual views only). */
export function tooBigToPreview(view: string, size: number): boolean {
  return isViewable(view) && ARTIFACT_VIEWS[view].textual && size > TEXT_VIEW_MAX
}

export const SVG_TYPE = 'image/svg+xml'

/** Whether an image file is an SVG (by its content type), which mediaUrl draws from a data: URL. */
export function isSvgImage(view: string, contentType: string): boolean {
  return view === 'image' && contentType.split(';')[0].trim().toLowerCase() === SVG_TYPE
}

function base64(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf)
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(s)
}

/** The URL an <img>, <audio>, <video> or <iframe> draws a file from: every viewer makes it here.
 *  An object URL of the allow-listed type (blobTypeFor), except an SVG, which becomes a
 *  `data:image/svg+xml;base64,` URL: a data: document has an opaque origin, and browsers block
 *  top-level navigation to one, so "Open image in new tab" cannot run its script as the app. */
export async function mediaUrl(bytes: Blob | ArrayBuffer, view: string, contentType: string): Promise<string> {
  if (isSvgImage(view, contentType)) {
    const buf = bytes instanceof Blob ? await bytes.arrayBuffer() : bytes
    return `data:${SVG_TYPE};base64,${base64(buf)}`
  }
  return URL.createObjectURL(new Blob([bytes], { type: blobTypeFor(view, contentType) }))
}

/** Frees a URL mediaUrl made (a data: URL holds nothing to free). */
export function releaseMediaUrl(url: string | null | undefined): void {
  if (url && url.startsWith('blob:')) URL.revokeObjectURL(url)
}
