import { describe, expect, it } from 'vitest'
import { splitHead, HIGHLIGHT_MAX_CHARS } from './highlight'

describe('splitHead', () => {
  it('keeps a small text whole', () => {
    expect(splitHead('a\nb')).toEqual({ head: 'a\nb', tail: '' })
  })
  it('cuts a big text after a newline, losing nothing', () => {
    const text = 'abcdefghi\n'.repeat(HIGHLIGHT_MAX_CHARS / 10 + 50)
    const { head, tail } = splitHead(text)
    expect(head.length).toBeLessThanOrEqual(HIGHLIGHT_MAX_CHARS)
    expect(head.endsWith('\n')).toBe(true)
    expect(head + tail).toBe(text)
  })
  it('cuts a single huge line at the cap', () => {
    const { head, tail } = splitHead('x'.repeat(HIGHLIGHT_MAX_CHARS + 5))
    expect(head).toHaveLength(HIGHLIGHT_MAX_CHARS)
    expect(tail).toBe('xxxxx')
  })
})
