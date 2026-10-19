import { createDiffAnchor, diffAnchorContains } from './diffSelection'
import type { Patch } from './patch'
import type { Anchor } from './review'

/** A display-only projection. Original provenance is kept in the persisted request.
 * Never guess a changed or repeated passage onto a new revision. */
export function projectReviewAnchor(
  patch: Patch,
  anchor: Anchor,
  artifactId: string,
): Anchor | null {
  const range = anchor.diff
  if (!range || anchor.kind !== 'text' || anchor.quote === undefined)
    return null
  if (range.artifactId === artifactId)
    return diffAnchorContains(patch, anchor, { ...range, line: range.start })
      ? anchor
      : null
  const lines = anchor.quote.split('\n')
  if (lines.length > 200 || anchor.quote.length > 32768) return null
  for (const side of [
    range.side,
    range.side === 'new' ? 'old' : 'new',
  ] as const) {
    let match: Anchor | null = null
    for (let fi = 0; fi < patch.files.length; fi++) {
      const file = patch.files[fi]
      if ((side === 'new' ? file.newPath : file.oldPath) !== range.file)
        continue
      for (let hi = 0; hi < file.hunks.length; hi++) {
        const sideLines = file.hunks[hi].lines.filter(
          (line) => line.kind !== 'note' && line[side] !== undefined,
        )
        for (let index = 0; index < sideLines.length; index++) {
          const number = sideLines[index][side]!
          if (
            !lines.every(
              (text, offset) =>
                sideLines[index + offset]?.text === text &&
                sideLines[index + offset]?.[side] === number + offset,
            )
          )
            continue
          const found = createDiffAnchor(
            patch,
            { fileIndex: fi + 1, hunk: hi + 1, side, line: number },
            number + lines.length - 1,
          )
          if (found?.quote !== anchor.quote || !found.diff) continue
          if (match) return null
          match = { ...found, diff: { ...found.diff, artifactId } }
        }
      }
    }
    if (match) return match
  }
  return null
}
