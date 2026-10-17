// The diff card is both an .artifact-preview (chat.css: line-height 0, display block — for images)
// and an .artifact-preview-diff (diff.css). The bundle's file order is not ours to choose, so the
// diff card's rule must outweigh the base rule by specificity, not by order.
import { describe, expect, it } from 'vitest'

// Read from disk the way theme/tokens.test.ts does: vitest blanks .css imports, and the package's
// tsconfig has no node types, so node:fs comes in by a dynamic import the type checker skips.
interface Fs { readFileSync(path: string, encoding: 'utf8'): string }
const fsName = 'node:fs'
const fs = (await import(/* @vite-ignore */ fsName)) as Fs
const DIR = (expect.getState().testPath ?? '').replace(/\/diff\.css\.test\.ts$/, '')

describe('diff.css', () => {
  it('styles the diff card with a selector stronger than .artifact-preview', () => {
    const css = fs.readFileSync(`${DIR}/diff.css`, 'utf8')
    expect(css).toMatch(/\.artifact-preview\.artifact-preview-diff\s*\{[^}]*line-height:\s*1\.45/)
    expect(css).not.toMatch(/(^|\n)\.artifact-preview-diff\s*\{/)
  })
})
