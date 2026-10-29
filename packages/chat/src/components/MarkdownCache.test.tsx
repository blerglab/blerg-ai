import { render } from '@testing-library/react'
import { beforeEach, expect, it } from 'vitest'
import Markdown, { markdownCacheForTest } from './Markdown'

// #306: switching back to a session parsed every visible message's Markdown again (~85-155 ms on a
// long session). A finished text renders the same every time: it is parsed once and reused.
beforeEach(() => markdownCacheForTest.clear())

it('parses a text once across unmounts and remounts', () => {
  const text = '## Plan\n\n- **one** `two`\n- [link](https://example.com)'
  const first = render(<Markdown text={text} />)
  const html = first.container.innerHTML
  first.unmount()
  expect(markdownCacheForTest.misses()).toBe(1)
  const again = render(<Markdown text={text} />)
  expect(markdownCacheForTest.misses()).toBe(1)
  expect(again.container.innerHTML).toBe(html)
})

it('keeps the variants apart: inline, raw HTML and plain text never share a result', () => {
  const text = 'a <b>b</b> **c**'
  const plain = render(<Markdown text={text} />).container.innerHTML
  const inline = render(<Markdown text={text} inline />).container.innerHTML
  const html = render(<Markdown text={text} html />).container.innerHTML
  expect(markdownCacheForTest.misses()).toBe(3)
  expect(new Set([plain, inline, html]).size).toBe(3)
})

it('forgets everything on a sign-in change or sign-out', () => {
  render(<Markdown text="**kept**" />).unmount()
  window.dispatchEvent(new Event('blerg-identity-changed'))
  render(<Markdown text="**kept**" />).unmount()
  expect(markdownCacheForTest.misses()).toBe(2)
  window.dispatchEvent(new Event('blerg-signed-out'))
  render(<Markdown text="**kept**" />).unmount()
  expect(markdownCacheForTest.misses()).toBe(3)
})

it('stays within its size bound and never keeps a huge text', () => {
  const huge = 'x'.repeat(300_000)
  render(<Markdown text={huge} />).unmount()
  render(<Markdown text={huge} />).unmount()
  expect(markdownCacheForTest.misses()).toBe(2)
  for (let i = 0; i < 400; i++) render(<Markdown text={`entry ${i} ${'y'.repeat(20_000)}`} />).unmount()
  expect(markdownCacheForTest.chars()).toBeLessThanOrEqual(4_000_000)
})
