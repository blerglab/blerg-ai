// What an inline preview of a text-like file shows: a short excerpt, cut from the file's text.
// Only the excerpt is cached (not the file), so twenty previews cost kilobytes, not megabytes.
import { parseCsv, prettyJson } from './artifacts'
import { langFromName, type CodeLang } from './codeLang'

/** Lines of markdown, text and code a preview shows. */
export const PREVIEW_LINES = 12
/** Rows and columns of a table preview. */
export const PREVIEW_ROWS = 6
export const PREVIEW_COLS = 6
/** A line is cut here: a minified file is one enormous line. */
const LINE_CHARS = 240

export type Excerpt =
  | { kind: 'markdown'; text: string; more: boolean }
  | { kind: 'text'; text: string; lang: CodeLang | null; more: boolean }
  | { kind: 'csv'; rows: string[][]; more: boolean }

function head(text: string): { text: string; more: boolean } {
  const lines = text.split('\n')
  const more = lines.length > PREVIEW_LINES && !(lines.length === PREVIEW_LINES + 1 && lines[PREVIEW_LINES] === '')
  return { text: lines.slice(0, PREVIEW_LINES).map(l => (l.length > LINE_CHARS ? l.slice(0, LINE_CHARS) + '…' : l)).join('\n'), more }
}

/** The excerpt of a file of this view, from its text (already within the preview size cap). */
export function excerptOf(view: string, name: string, contentType: string, text: string): Excerpt {
  if (text.charCodeAt(0) === 0xfeff) text = text.slice(1)
  if (view === 'csv') {
    const tab = /\.tsv$/i.test(name) || contentType.includes('tab-separated')
    const { rows, moreRows, moreCols } = parseCsv(text, tab ? '\t' : ',', PREVIEW_ROWS, PREVIEW_COLS)
    return { kind: 'csv', rows, more: moreRows || moreCols }
  }
  if (view === 'markdown') return { kind: 'markdown', ...head(text) }
  if (view === 'json') return { kind: 'text', lang: 'json', ...head(prettyJson(text)) }
  return { kind: 'text', lang: langFromName(name), ...head(text) }
}
