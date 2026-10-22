// Raw HTML in Markdown: on a document surface (`html`) GitHub's allowlist renders and everything else
// is neutralized; everywhere else HTML stays literal text. Every corpus entry carries a harmless
// <b>kept</b> beside the payload, so each document case also proves the HTML path runs (without it,
// raw HTML was literal text and these would have nothing to check).
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import Markdown, { type MarkdownDocument } from './Markdown'
import { ChatContext, type ChatContextValue } from './context'
import type { WorkspaceFile } from '../transport/types'

declare global { interface Window { __xss?: number } }

const CORPUS: Array<[string, string]> = [
  ['script', '<script>window.__xss=1</script>'],
  ['img onerror', '<img src=x onerror="window.__xss=1">'],
  ['img onerror, unquoted', '<IMG SRC=/ onerror=window.__xss=1>'],
  ['javascript: href', '<a href="javascript:window.__xss=1">j</a>'],
  ['javascript: href, entity and tab', '<a href="jav&#x09;ascript:window.__xss=1">j</a>'],
  ['javascript: href, upper case and spaces', '<a href="  JAVASCRIPT:window.__xss=1">j</a>'],
  ['vbscript: href', '<a href="vbscript:msgbox(1)">v</a>'],
  ['data:text/html href', '<a href="data:text/html,<script>window.__xss=1</script>">d</a>'],
  ['data:text/html base64 href', '<a href="data:text/html;base64,PHNjcmlwdD53aW5kb3cuX194c3M9MTwvc2NyaXB0Pg==">d</a>'],
  ['svg onload', '<svg onload="window.__xss=1"><circle r="5"/></svg>'],
  ['svg image', '<svg><image href="https://t.example/p.png"/></svg>'],
  ['svg data image', '<img src="data:image/svg+xml,<svg xmlns=%22http://www.w3.org/2000/svg%22 onload=%22window.__xss=1%22/>">'],
  ['style element', '<style>body{display:none}</style>'],
  ['style attribute', '<p style="background:url(https://t.example/p.png)">s</p>'],
  ['iframe', '<iframe src="https://evil.example"></iframe>'],
  ['iframe srcdoc', '<iframe srcdoc="<script>parent.__xss=1</script>"></iframe>'],
  ['form', '<form action="https://evil.example"><input name="q"><button formaction="javascript:window.__xss=1">go</button></form>'],
  ['meta refresh', '<meta http-equiv="refresh" content="0;url=https://evil.example">'],
  ['base href', '<base href="https://evil.example/">'],
  ['object and embed', '<object data="https://evil.example/x.swf"></object><embed src="https://evil.example/x.swf">'],
  ['link stylesheet', '<link rel="stylesheet" href="https://evil.example/x.css">'],
  ['mXSS: noscript', '<noscript><p title="</noscript><img src=x onerror=window.__xss=1>"></noscript>'],
  ['mXSS: style in svg', '<svg><style><img src=x onerror=window.__xss=1></style></svg>'],
  ['mXSS: math mglyph', '<math><mtext><table><mglyph><style><img src=x onerror=window.__xss=1>'],
  ['mXSS: textarea', '<textarea><img title="</textarea><img src=x onerror=window.__xss=1>"></textarea>'],
  ['mXSS: comment', '<!--<img src="--><img src=x onerror=window.__xss=1//">'],
  ['mXSS: template', '<template><img src=x onerror=window.__xss=1></template>'],
  ['attribute quote breaking', `<img alt="x" title='a" onerror="window.__xss=1'>`],
  ['attribute quote breaking, entity', '<div title="&quot; onmouseover=&quot;window.__xss=1">t</div>'],
  ['attribute quote breaking, backtick', '<img alt=`x` src=`x` onerror=`window.__xss=1`>'],
  ['details ontoggle', '<details open ontoggle="window.__xss=1"><summary>s</summary>x</details>'],
  ['id and name clobbering', '<img name="getElementById" id="root"><a id="app" name="x" href="#">c</a>'],
  ['class and data attributes', '<div class="artifact-overlay" data-testid="evil" aria-label="x">c</div>'],
  ['protocol-relative', '<a href="//evil.example/x">p</a><img src="//evil.example/p.png">'],
  ['backslash host', '<a href="\\\\evil.example/x">p</a><a href="/\\evil.example/x">q</a>'],
  ['srcset', '<img srcset="https://t.example/p.png 1x" alt="s">'],
  ['picture source', '<picture><source srcset="https://t.example/p.png"><img alt="pic"></picture>'],
  ['video and audio', '<video src="https://t.example/v.mp4" autoplay></video><audio src="https://t.example/a.mp3" autoplay></audio>'],
  ['input autofocus', '<input autofocus onfocus="window.__xss=1">'],
  ['marquee', '<marquee onstart="window.__xss=1">m</marquee>'],
]

const FORBIDDEN = 'script, style, iframe, form, input, textarea, button:not(.md-img-load):not(.md-copy):not(.md-codeblock-more), meta, base, link, object, embed, svg, math, noscript, template, video, audio, source, marquee'

function invariants(root: HTMLElement) {
  expect(root.querySelectorAll(FORBIDDEN)).toHaveLength(0)
  for (const el of Array.from(root.querySelectorAll('*'))) {
    for (const attr of Array.from(el.attributes)) {
      const name = attr.name.toLowerCase()
      expect(name.startsWith('on'), `${el.tagName} ${name}`).toBe(false)
      expect(['style', 'srcdoc', 'srcset', 'formaction', 'action', 'name'], `${el.tagName} ${name}`).not.toContain(name)
      if (name === 'id') expect(attr.value).toMatch(/^user-content-/)
      if (name === 'href') expect(attr.value).toMatch(/^(https?:\/\/|#)/i)
      if (name === 'src') expect(attr.value).toMatch(/^(blob:|data:image\/(png|jpeg|jpg|gif|webp);base64,)/i)
    }
  }
  expect(window.__xss).toBeUndefined()
}

const doc: MarkdownDocument = {}

beforeEach(() => { delete window.__xss })
afterEach(() => { delete window.__xss })

describe('raw HTML: the XSS corpus is neutralized', () => {
  it.each(CORPUS)('%s, in chat text (literal)', async (_name, payload) => {
    const { container } = render(<Markdown text={`<b>kept</b>${payload}\n\nAfter.`} />)
    expect(container.querySelector('b')).toBeNull()
    expect(container.textContent).toContain('<b>kept</b>')
    await new Promise(r => setTimeout(r, 0))
    invariants(container)
  })
  it.each(CORPUS)('%s, in a document', async (_name, payload) => {
    const { container } = render(<Markdown className="md-doc" html document={doc} text={`<b>kept</b>${payload}\n\nAfter.`} />)
    expect(container.querySelector('b')?.textContent).toBe('kept')
    await new Promise(r => setTimeout(r, 0))
    invariants(container)
  })
})

describe('raw HTML: GitHub’s allowlist renders', () => {
  it('renders a centered paragraph, a table with widths, a details block wrapping Markdown, and keys', () => {
    const text = [
      '<p align="center"><b>One place</b> to run agents.<br><sub>small</sub> <kbd>Ctrl</kbd></p>',
      '',
      '<table align="center"><tr><td align="center" width="50%" colspan="2">cell</td></tr></table>',
      '',
      '<details><summary>More</summary>',
      '',
      'Hidden **markdown** here.',
      '',
      '</details>',
    ].join('\n')
    const { container } = render(<Markdown className="md-doc" html document={doc} text={text} />)
    const p = container.querySelector('p[align="center"]')!
    expect(p.querySelector('b')?.textContent).toBe('One place')
    expect(p.querySelector('br')).not.toBeNull()
    expect(p.querySelector('sub')?.textContent).toBe('small')
    expect(p.querySelector('kbd')?.textContent).toBe('Ctrl')
    const td = container.querySelector('table[align="center"] td')!
    expect(td.getAttribute('width')).toBe('50%')
    expect(td.getAttribute('colspan')).toBe('2')
    const details = container.querySelector('details')!
    expect(details.querySelector('summary')?.textContent).toBe('More')
    expect(details.querySelector('strong')?.textContent).toBe('markdown')
    expect(container.textContent).not.toContain('<p')
  })

  it('renders inline HTML around Markdown inside a paragraph', () => {
    const { container } = render(<Markdown html text={'Press <kbd>Ctrl</kbd>+<kbd>K</kbd>, then <b>**bold** text</b>.'} />)
    expect(container.querySelectorAll('kbd')).toHaveLength(2)
    expect(container.querySelector('b strong')?.textContent).toBe('bold')
  })
})

describe('images', () => {
  const realCreate = URL.createObjectURL
  const realRevoke = URL.revokeObjectURL
  beforeEach(() => {
    URL.createObjectURL = vi.fn(() => 'blob:http://localhost/img-1')
    URL.revokeObjectURL = vi.fn()
  })
  afterEach(() => {
    URL.createObjectURL = realCreate
    URL.revokeObjectURL = realRevoke
  })
  const png = (path: string): WorkspaceFile => ({ path, size: 4, view: 'image', content_type: 'image/png', mtime: '', blob: new Blob(['png!'], { type: 'image/png' }) })

  it('loads a relative image from the folder, beside the document', async () => {
    const read = vi.fn(async (p: string) => png(p))
    const { container } = render(<Markdown className="md-doc" html document={{ path: 'docs/guide.md', readWorkspace: read }} text={'<img src="../brand/banner.png" alt="Banner" width="100%">\n\n![shot](img/a.png)'} />)
    await waitFor(() => expect(container.querySelectorAll('img')).toHaveLength(2))
    expect(read.mock.calls.map(c => c[0]).sort()).toEqual(['brand/banner.png', 'docs/img/a.png'])
    const banner = screen.getByAltText('Banner')
    expect(banner.getAttribute('src')).toBe('blob:http://localhost/img-1')
    expect(banner.getAttribute('width')).toBe('100%')
  })

  it('shows a placeholder when the folder file is not an image', async () => {
    const read = vi.fn(async (p: string) => ({ ...png(p), view: 'html' }))
    render(<Markdown className="md-doc" html document={{ path: 'README.md', readWorkspace: read }} text={'<img src="x.html" alt="Not an image">'} />)
    await waitFor(() => expect(read).toHaveBeenCalled())
    expect(await screen.findByTestId('md-img-placeholder')).toHaveTextContent('Not an image')
    expect(document.querySelector('img')).toBeNull()
    await new Promise(r => setTimeout(r, 0))
    expect(screen.queryByRole('button', { name: /^Retry/ })).toBeNull() // reading it again cannot help
  })

  it('draws Retry after the link an image sits in, never inside it', async () => {
    const read = vi.fn(async () => { throw Object.assign(new Error('gone'), { status: 404, code: 'not_found' }) })
    render(<Markdown className="md-doc" html document={{ path: 'README.md', readWorkspace: read }} text={'<a href="https://example.com/video"><img src="thumb.jpg" alt="Watch"></a>'} />)
    const retry = await screen.findByRole('button', { name: 'Retry Watch' })
    expect(retry.closest('a')).toBeNull()
    expect(screen.getByTestId('md-img-placeholder').closest('a')).not.toBeNull()
  })

  it('offers Retry on an image whose read really failed, and reads it again', async () => {
    let calls = 0
    const read = vi.fn(async (p: string) => {
      if (++calls === 1) throw Object.assign(new Error('not found'), { status: 404, serverText: 'The workspace file was not found.' })
      return png(p)
    })
    render(<Markdown className="md-doc" html document={{ path: 'README.md', readWorkspace: read }} text={'<img src="late.png" alt="Late">'} />)
    const retry = await screen.findByRole('button', { name: /^Retry/ })
    expect(screen.getByTestId('md-img-placeholder')).toHaveTextContent('Late')
    fireEvent.click(retry)
    await screen.findByAltText('Late')
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('never reads a path outside the folder', () => {
    const read = vi.fn(async (p: string) => png(p))
    render(<Markdown className="md-doc" html document={{ path: 'README.md', readWorkspace: read }} text={'<img src="../../etc/passwd" alt="up">'} />)
    expect(read).not.toHaveBeenCalled()
    expect(screen.getByTestId('md-img-placeholder')).toHaveTextContent('up')
  })

  it('a published document (no folder) shows the alt text for a relative image', () => {
    render(<Markdown className="md-doc" html document={doc} text={'<img src="brand/banner.png" alt="Banner">'} />)
    expect(screen.getByTestId('md-img-placeholder')).toHaveTextContent('Banner')
    expect(document.querySelector('img')).toBeNull()
  })

  it('fetches a remote image only when the person clicks Load image, without a referrer', () => {
    render(<Markdown className="md-doc" html document={doc} text={'<a href="https://example.com/r"><img alt="Release" src="https://img.example/badge.svg"></a>'} />)
    expect(document.querySelector('img')).toBeNull()
    expect(screen.getByTestId('md-img-placeholder')).toHaveTextContent('Release')
    fireEvent.click(screen.getByRole('button', { name: 'Load image' }))
    const img = screen.getByAltText('Release')
    expect(img.getAttribute('src')).toBe('https://img.example/badge.svg')
    expect(img.getAttribute('referrerpolicy')).toBe('no-referrer')
  })

  it('draws a base64 raster data image as it is', () => {
    render(<Markdown className="md-doc" html document={doc} text={'<img alt="dot" src="data:image/png;base64,iVBORw0KGgo=">'} />)
    expect(screen.getByAltText('dot').getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
  })

  it('chat text never loads an image: Markdown images are links, HTML is literal', () => {
    const { container } = render(<Markdown text={'<img alt="px" src="https://t.example/p.png"> ![md](https://t.example/q.png) <img alt="rel" src="a.png">'} />)
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('.md-img-load')).toBeNull()
    expect(container.textContent).toContain('<img alt="px" src="https://t.example/p.png">')
    expect(container.textContent).toContain('[md]')
  })

  it('shows the host a remote image would be loaded from, before the click', () => {
    render(<Markdown className="md-doc" html document={doc} text={'<img alt="CI" src="https://img.shields.io/badge/x.svg">'} />)
    expect(screen.getByTestId('md-img-placeholder')).toHaveTextContent('img.shields.io')
    fireEvent.click(screen.getByRole('button', { name: 'Load image' }))
    expect(screen.getByAltText('CI').getAttribute('loading')).toBe('lazy')
  })

  it('reads at most 50 folder images by itself; "Load images" reads the rest', async () => {
    const read = vi.fn(async (p: string) => png(p))
    const text = Array.from({ length: 53 }, (_, i) => `<img src="img/${i}.png" alt="i${i}">`).join('\n')
    const { container } = render(<Markdown className="md-doc" html document={{ path: 'README.md', readWorkspace: read }} text={text} />)
    await waitFor(() => expect(container.querySelectorAll('img')).toHaveLength(50))
    expect(read).toHaveBeenCalledTimes(50)
    const more = screen.getAllByRole('button', { name: 'Load images' })
    expect(more).toHaveLength(3)
    fireEvent.click(more[0])
    await waitFor(() => expect(container.querySelectorAll('img')).toHaveLength(53))
    expect(read).toHaveBeenCalledTimes(53)
  })

  it('draws a folder SVG from a data: URL, never a same-origin blob: one', async () => {
    URL.createObjectURL = vi.fn(() => 'blob:http://localhost/svg')
    const read = vi.fn(async (p: string) => ({ ...png(p), content_type: 'image/svg+xml', blob: new Blob(['<svg xmlns="http://www.w3.org/2000/svg"/>']) }))
    render(<Markdown className="md-doc" html document={{ path: 'README.md', readWorkspace: read }} text={'<img src="logo.svg" alt="Logo">'} />)
    const img = await screen.findByAltText('Logo')
    expect(img.getAttribute('src')).toMatch(/^data:image\/svg\+xml;base64,/)
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })
})

describe('links', () => {
  function withChat(ui: React.ReactNode, open = vi.fn()) {
    const value = { sessionId: 's', files: null, view: vi.fn(), byName: () => undefined, latestVersion: () => undefined, openWorkspaceFile: open } as unknown as ChatContextValue
    render(<ChatContext.Provider value={value}>{ui}</ChatContext.Provider>)
    return open
  }

  it('opens a relative link from a folder file as the folder’s file, beside it', () => {
    const read = vi.fn()
    const open = withChat(<Markdown className="md-doc" html document={{ path: 'docs/a.md', readWorkspace: read }} text={'<a href="../AGENTS.md">agents</a> and [guide](install/desktop/README.md#L3)'} />)
    fireEvent.click(screen.getByText('agents'))
    expect(open).toHaveBeenLastCalledWith('AGENTS.md', undefined, { folder: true })
    fireEvent.click(screen.getByText('guide'))
    expect(open).toHaveBeenLastCalledWith('docs/install/desktop/README.md', 3, { folder: true })
  })

  it('keeps a published document’s relative link as text: it has no folder', () => {
    const open = withChat(<Markdown className="md-doc" html document={doc} text={'<a href="config/prod.yaml">Open the plan</a> and [also](config/x.yaml)'} />)
    for (const name of ['Open the plan', 'also']) {
      const el = screen.getByText(name)
      expect(el.closest('a')).toBeNull()
      fireEvent.click(el)
    }
    expect(open).not.toHaveBeenCalled()
    expect(screen.getByText('also').getAttribute('title')).toMatch(/no folder/)
  })

  it('opens an external link in a new tab with noopener noreferrer', () => {
    withChat(<Markdown className="md-doc" html document={doc} text={'<a href="https://github.com/x">gh</a>'} />)
    const a = screen.getByText('gh').closest('a')!
    expect(a.getAttribute('target')).toBe('_blank')
    expect(a.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('scrolls to a heading for a #fragment, by GitHub’s id', () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    withChat(<Markdown className="md-doc" html document={doc} text={'[jump](#what-you-can-do)\n\n## What you can do\n\ntext'} />)
    const heading = screen.getByRole('heading', { name: 'What you can do' })
    expect(heading.id).toBe('user-content-what-you-can-do')
    fireEvent.click(screen.getByText('jump'))
    expect(scroll).toHaveBeenCalled()
    expect(scroll.mock.contexts[0]).toBe(heading)
  })
})
