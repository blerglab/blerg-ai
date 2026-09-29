import { describe, expect, it } from 'vitest'
import { render } from '@testing-library/react'
import Markdown from './Markdown'
import { safeUrl } from '../lib/safeUrl'

function md(text: string) {
  return render(<Markdown text={text} />).container
}

describe('Markdown', () => {
  it('renders bold, italic and inline code', () => {
    const c = md('this is **bold**, *italic* and `code`')
    expect(c.querySelector('strong')?.textContent).toBe('bold')
    expect(c.querySelector('em')?.textContent).toBe('italic')
    expect(c.querySelector('code')?.textContent).toBe('code')
    expect(c.querySelector('pre')).toBeNull()
  })

  it('renders fenced code blocks in a monospace pre with a copy affordance', () => {
    Object.assign(navigator, { clipboard: { writeText: () => Promise.resolve() } })
    const c = md('before\n\n```go\nfunc main() {}\n```\n\nafter')
    const pre = c.querySelector('.md-codeblock pre')
    expect(pre).not.toBeNull()
    expect(pre?.textContent).toContain('func main() {}')
    expect(c.querySelector('.md-copy')).not.toBeNull()
  })

  it('renders lists, headings and blockquotes', () => {
    const c = md('# Title\n\n- one\n- two\n\n1. first\n2. second\n\n> quoted')
    expect(c.querySelector('h1')?.textContent).toBe('Title')
    expect(c.querySelectorAll('ul li')).toHaveLength(2)
    expect(c.querySelectorAll('ol li')).toHaveLength(2)
    expect(c.querySelector('blockquote')?.textContent).toContain('quoted')
  })

  it('renders GFM tables inside a scroll wrapper', () => {
    const c = md('| a | b |\n|---|---|\n| 1 | 2 |')
    expect(c.querySelector('.md-table-wrap table')).not.toBeNull()
    expect(c.querySelectorAll('td')).toHaveLength(2)
  })

  it('opens http links in a new tab with noopener noreferrer', () => {
    const c = md('[docs](https://example.com/x)')
    const a = c.querySelector('a')!
    expect(a.getAttribute('href')).toBe('https://example.com/x')
    expect(a.getAttribute('target')).toBe('_blank')
    expect(a.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('never renders a javascript: link', () => {
    const c = md('[click](javascript:alert(1)) and [data](data:text/html,<b>x</b>)')
    expect(c.querySelector('a')).toBeNull()
    expect(c.textContent).toContain('click')
    expect(c.innerHTML).not.toContain('javascript:')
  })

  it('keeps raw HTML inert — shown as text, never elements', () => {
    const c = md('<script>window.__pwned = 1</script>\n\nhi <img src=x onerror="window.__pwned=2"> there')
    expect(c.querySelector('script')).toBeNull()
    expect(c.querySelector('img')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
    expect(c.textContent).toContain('hi')
    expect(c.textContent).toContain('there')
  })

  it('never loads markdown images; links to them instead', () => {
    const c = md('![diagram](https://example.com/a.png)')
    expect(c.querySelector('img')).toBeNull()
    expect(c.querySelector('a')?.getAttribute('href')).toBe('https://example.com/a.png')
  })

  it('renders an unclosed fence while streaming without throwing', () => {
    const { container, rerender } = render(<Markdown text={'Here:\n\n```ts\nconst a = 1'} />)
    expect(container.querySelector('pre')?.textContent).toContain('const a = 1')
    rerender(<Markdown text={'Here:\n\n```ts\nconst a = 1\nconst b = **2'} />)
    expect(container.querySelector('pre')?.textContent).toContain('const b = **2')
    rerender(<Markdown text={'Here:\n\n```ts\nconst a = 1\n```\n\nand **bo'} />)
    expect(container.querySelector('pre')?.textContent).toContain('const a = 1')
    expect(container.textContent).toContain('and **bo')
  })
})

describe('safeUrl', () => {
  it.each([
    ['https://a.b/c', 'https://a.b/c'],
    ['http://a.b', 'http://a.b'],
    ['mailto:x@y.z', 'mailto:x@y.z'],
    ['javascript:alert(1)', null],
    ['JaVaScRiPt:alert(1)', null],
    [' javascript:alert(1)', null],
    ['data:text/html,x', null],
    ['vbscript:x', null],
    ['/relative/path', null],
    ['', null],
  ])('%s → %s', (input, want) => {
    expect(safeUrl(input)).toBe(want)
  })
})
