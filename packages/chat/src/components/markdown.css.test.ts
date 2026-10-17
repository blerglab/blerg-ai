// The code viewer's gutter and its text must share one line height. Apps style a bare `code` as they
// like (the runner: 15px, line-height 135%, padding, a background), so the viewer's code element has
// to reset all of that and inherit from the pane — or the line numbers drift from their lines.
import { describe, expect, it } from 'vitest'

interface Fs { readFileSync(path: string, encoding: 'utf8'): string }
const fsName = 'node:fs'
const fs = (await import(/* @vite-ignore */ fsName)) as Fs
const DIR = (expect.getState().testPath ?? '').replace(/\/markdown\.css\.test\.ts$/, '')

describe('markdown.css', () => {
  it("resets the code viewer's code element so it sits on the gutter's lines", () => {
    const css = fs.readFileSync(`${DIR}/markdown.css`, 'utf8')
    const rule = [...css.matchAll(/\.code-viewer-code code\s*\{([^}]*)\}/g)].map(m => m[1]).join('')
    for (const decl of ['padding: 0', 'background: none', 'font: inherit', 'line-height: inherit']) {
      expect(rule).toContain(decl)
    }
  })
})
