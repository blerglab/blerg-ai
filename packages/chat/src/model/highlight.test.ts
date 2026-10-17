import { afterEach, describe, expect, it, vi } from 'vitest'
import { highlightSync, loadHighlighter, splitTokenLines, testing, type Token } from './highlight'

const fake = vi.fn((_l: string, text: string): Token[] => [{ cls: 'hljs-keyword', kids: [{ text }] }])

afterEach(() => { testing.reset(); fake.mockClear() })

describe('highlight', () => {
  it('answers null until the highlighter has loaded, then tokens', async () => {
    testing.reset(async () => ({ highlightTokens: fake }))
    expect(highlightSync('go', 'func')).toBeNull()
    await loadHighlighter()
    expect(highlightSync('go', 'func')).toEqual([{ cls: 'hljs-keyword', kids: [{ text: 'func' }] }])
  })

  it('caches by language and text', async () => {
    testing.reset(async () => ({ highlightTokens: fake }))
    await loadHighlighter()
    highlightSync('go', 'a'); highlightSync('go', 'a'); highlightSync('python', 'a')
    expect(fake).toHaveBeenCalledTimes(2)
  })

  it('shows huge text plain', async () => {
    testing.reset(async () => ({ highlightTokens: fake }))
    await loadHighlighter()
    expect(highlightSync('go', 'x'.repeat(100_001))).toBeNull()
  })

  it('survives a highlighter that throws', async () => {
    testing.reset(async () => ({ highlightTokens: () => { throw new Error('boom') } }))
    await loadHighlighter()
    expect(highlightSync('go', 'x')).toBeNull()
  })

  it('the real grammars produce tokens, not markup', async () => {
    const { highlightTokens } = await import('./highlighter')
    const toks = highlightTokens('go', 'func main() { return "<b>x</b>" }')
    const flat = JSON.stringify(toks)
    expect(flat).toContain('hljs-keyword')
    expect(flat).toContain('<b>x</b>') // as text, in a string token
    expect(toks.some(t => 'cls' in t)).toBe(true)
  })
})

describe('splitTokenLines', () => {
  it('cuts at newlines and repeats a span that crosses one', () => {
    const lines = splitTokenLines([
      { text: 'a\n' },
      { cls: 'hljs-comment', kids: [{ text: '/* x\ny */' }] },
      { text: ' b' },
    ])
    expect(lines).toEqual([
      [{ text: 'a' }],
      [{ cls: 'hljs-comment', kids: [{ text: '/* x' }] }],
      [{ cls: 'hljs-comment', kids: [{ text: 'y */' }] }, { text: ' b' }],
    ])
  })
})
