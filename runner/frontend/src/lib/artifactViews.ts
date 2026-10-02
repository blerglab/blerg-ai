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
  json: { label: 'JSON', textual: true },
  csv: { label: 'Table', textual: true },
  html: { label: 'HTML', textual: true },
  // SVG is an image here and is ONLY ever shown through <img src=blob:>, where script never runs.
  image: { label: 'Image', textual: false, blobTypes: /^image\/(png|jpeg|gif|webp|svg\+xml)$/ },
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
 *  ALWAYS application/pdf: the server's word is not taken for a type the browser would render. */
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
