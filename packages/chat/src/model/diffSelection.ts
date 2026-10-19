import type { Patch } from './patch'
import type { Anchor } from './review'

export interface DiffPoint {
  /** All indices and line numbers are 1-based. */
  fileIndex: number
  hunk: number
  side: 'old' | 'new'
  line: number
}
export const MAX_DIFF_SELECTION_LINES = 200
export const MAX_DIFF_SELECTION_QUOTE = 32_768
const positive = (value: number) => Number.isSafeInteger(value) && value > 0
const hasControl = (text: string) => Array.from(text).some(char => {
  const code = char.charCodeAt(0)
  return code < 32 || (code >= 127 && code <= 159)
})

/** A quote from one side of one hunk. No path is resolved or fetched. */
export function createDiffAnchor(patch: Patch, point: DiffPoint, endLine = point.line): Anchor | null {
  if (![point.fileIndex, point.hunk, point.line, endLine].every(positive) || !['old', 'new'].includes(point.side)) return null
  const start = Math.min(point.line, endLine)
  const end = Math.max(point.line, endLine)
  if (end - start >= MAX_DIFF_SELECTION_LINES) return null
  const file = patch.files[point.fileIndex - 1]
  const hunk = file?.hunks[point.hunk - 1]
  const path = point.side === 'old' ? file?.oldPath : file?.newPath
  if (!hunk || !path || path.length > 4096 || hasControl(path)) return null
  const picked = hunk.lines.filter(line => line.kind !== 'note' && line[point.side] !== undefined && line[point.side]! >= start && line[point.side]! <= end)
  if (picked.length !== end - start + 1 || picked.some((line, i) => line[point.side] !== start + i)) return null
  const quote = picked.map(line => line.text).join('\n')
  if (quote.length > MAX_DIFF_SELECTION_QUOTE) return null
  const commit = file.commit === undefined ? undefined : patch.commits[file.commit]?.sha
  return {kind: 'text', quote, diff: {file: path, side: point.side, start, end,
    hunk: point.hunk, fileIndex: point.fileIndex, ...(commit ? {commit} : {})}}
}

/** Validate the complete quote/identity before highlighting an existing request. Call once
 * per anchor (then render its range), rather than scanning the hunk for every painted cell. */
export function diffAnchorContains(patch: Patch, anchor: Anchor | null | undefined, point: DiffPoint): boolean {
  if (![point.fileIndex, point.hunk, point.line].every(positive)) return false
  const range = anchor?.diff
  if (!range || anchor.kind !== 'text' || range.fileIndex !== point.fileIndex || range.hunk !== point.hunk || range.side !== point.side || point.line < range.start || point.line > range.end) return false
  const current = createDiffAnchor(patch, {...point, line: range.start}, range.end)
  return !!current?.diff && current.quote === anchor.quote && current.diff.file === range.file && current.diff.commit === range.commit && current.diff.start === range.start && current.diff.end === range.end
}
