import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'
import { HIGHLIGHT_MAX_CHARS } from '../model/highlight'
import { DIFF_FILE_LINES } from './DiffView'
import SourceCode from './SourceCode'

const note = 'Large diff shown as plain text to keep scrolling responsive.'

function source(text: string) {
  const html = renderToStaticMarkup(<SourceCode text={text} name="changes.patch" />)
  return { html, code: html.match(/<code>([\s\S]*?)<\/code>/)?.[1] }
}

describe('Raw diff highlighting limits', () => {
  it.each([
    ['character limit', `+${'x'.repeat(HIGHLIGHT_MAX_CHARS)}`],
    ['line limit', '+x\n'.repeat(DIFF_FILE_LINES + 1)],
    ['large preview', 'diff --git a/big.txt b/big.txt\n--- /dev/null\n+++ b/big.txt\n@@ -0,0 +1,150000 @@\n' + '+hello world\n'.repeat(150_000)],
  ])('shows plain source past the %s without creating per-line spans', (_label, text) => {
    const { html, code } = source(text)
    expect((code?.match(/<span/g) ?? []).length).toBe(0)
    expect(code).toBe(text)
    expect(html).toContain(note)
  })

  it.each([
    ['character limit', `+${'x'.repeat(HIGHLIGHT_MAX_CHARS - 1)}`],
    ['line limit', '+x\n'.repeat(DIFF_FILE_LINES)],
  ])('still colors source exactly at the %s', (_label, text) => {
    const { html } = source(text)
    expect(html).toContain('class="md-diff-add"')
    expect(html).not.toContain(note)
  })
})
