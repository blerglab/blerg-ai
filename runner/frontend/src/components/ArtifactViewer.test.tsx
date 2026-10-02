import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ArtifactViewer from './ArtifactViewer'
import { HTML_ARTIFACT_CSP } from '../lib/artifacts'
import type { ArtifactInfo } from '../lib/artifacts'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...a: unknown[]) => apiFetch(...a) }))

let blobs: Blob[]
let revoked: string[]
let nextUrl: number

function art(over: Partial<ArtifactInfo>): ArtifactInfo {
  return { id: 'a1', name: 'notes.txt', size: 100, content_type: 'text/plain; charset=utf-8', view: 'text', ...over }
}

function serve(body: BodyInit | null, status = 200) {
  apiFetch.mockImplementation(() => Promise.resolve(new Response(body, { status })))
}

function show(a: ArtifactInfo, onClose = vi.fn()) {
  return { onClose, ...render(<ArtifactViewer sessionId="s1" artifact={a} onClose={onClose} />) }
}

beforeEach(() => {
  apiFetch.mockReset()
  blobs = []
  revoked = []
  nextUrl = 0
  Object.assign(URL, {
    createObjectURL: vi.fn((b: Blob) => { blobs.push(b); return `blob:test-${nextUrl++}` }),
    revokeObjectURL: vi.fn((u: string) => { revoked.push(u) }),
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('ArtifactViewer: text', () => {
  it('fetches /raw through apiFetch and shows the text as text, never as markup', async () => {
    serve('hello <b>world</b>\n<script>window.__pwned = 1</script>')
    const { container } = show(art({}))
    const pre = await screen.findByTestId('artifact-text')
    expect(pre.textContent).toContain('<script>window.__pwned = 1</script>')
    expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/artifacts/a1/raw')
    expect(container.ownerDocument.querySelector('script')).toBeNull()
    expect(pre.querySelector('b')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
  })

  it('caps a long file with a Show all', async () => {
    serve('x'.repeat(300 * 1024))
    show(art({ size: 300 * 1024 }))
    const pre = await screen.findByTestId('artifact-text')
    expect(pre.textContent!.length).toBe(200 * 1024)
    expect(screen.getByText(/Showing the first 200 KB/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show all' }))
    expect(screen.getByTestId('artifact-text').textContent!.length).toBe(300 * 1024)
    expect(screen.queryByRole('button', { name: 'Show all' })).toBeNull()
  })

  it('offers only a download for a text file over 2 MiB, without fetching it', () => {
    show(art({ size: 3 * 1024 * 1024 }))
    expect(screen.getByTestId('artifact-no-preview')).toHaveTextContent('Too large to preview')
    expect(screen.getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(apiFetch).not.toHaveBeenCalled()
  })

  it('says so when the file cannot be loaded, and still offers the download', async () => {
    serve('{}', 500)
    show(art({}))
    expect(await screen.findByTestId('artifact-error')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Download' })).toBeInTheDocument()
  })
})

describe('ArtifactViewer: markdown', () => {
  it('renders markdown and does not let a script tag through', async () => {
    serve('# Title\n\nsome **bold** text\n\n<script>window.__pwned = 1</script>\n\n<img src=x onerror="window.__pwned=2">')
    const { container } = show(art({ name: 'r.md', view: 'markdown', content_type: 'text/markdown' }))
    expect(await screen.findByRole('heading', { name: 'Title' })).toBeInTheDocument()
    expect(container.ownerDocument.querySelector('script')).toBeNull()
    expect(container.ownerDocument.querySelector('img')).toBeNull()
    expect(document.body.textContent).toContain('<script>window.__pwned = 1</script>') // shown as the text it is
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
  })
})

describe('ArtifactViewer: json', () => {
  it('pretty-prints', async () => {
    serve('{"a":1,"b":[true]}')
    show(art({ name: 'd.json', view: 'json', content_type: 'application/json' }))
    const pre = await screen.findByTestId('artifact-text')
    expect(pre.textContent).toBe('{\n  "a": 1,\n  "b": [\n    true\n  ]\n}')
  })

  it('falls back to the raw text when it does not parse', async () => {
    serve('{"a": 1} trailing')
    show(art({ name: 'd.json', view: 'json', content_type: 'application/json' }))
    expect((await screen.findByTestId('artifact-text')).textContent).toBe('{"a": 1} trailing')
  })
})

describe('ArtifactViewer: csv', () => {
  const csv = (over: Partial<ArtifactInfo> = {}) => art({ name: 'd.csv', view: 'csv', content_type: 'text/csv', ...over })

  it('shows a table, with quoted commas, newlines and quotes intact and cells as text', async () => {
    serve('name,note\n"Smith, J","two\nlines"\n"say ""hi""",<b>x</b>\n')
    show(csv())
    const table = await screen.findByTestId('artifact-table')
    const rows = within(table).getAllByRole('row')
    expect(rows).toHaveLength(3)
    expect(within(rows[0]).getAllByRole('columnheader').map(c => c.textContent)).toEqual(['name', 'note'])
    expect(within(rows[1]).getAllByRole('cell').map(c => c.textContent)).toEqual(['Smith, J', 'two\nlines'])
    expect(within(rows[2]).getAllByRole('cell').map(c => c.textContent)).toEqual(['say "hi"', '<b>x</b>'])
    expect(table.querySelector('b')).toBeNull()
  })

  it('caps at 500 rows and says how many it shows', async () => {
    serve(Array.from({ length: 700 }, (_, i) => `r${i},v`).join('\n'))
    show(csv())
    const table = await screen.findByTestId('artifact-table')
    expect(within(table).getAllByRole('row')).toHaveLength(500)
    expect(screen.getByText(/Showing the first 500 rows/)).toBeInTheDocument()
  })

  it('caps at 50 columns', async () => {
    serve(Array.from({ length: 60 }, (_, i) => `c${i}`).join(',') + '\n' + Array.from({ length: 60 }, (_, i) => i).join(','))
    show(csv())
    const table = await screen.findByTestId('artifact-table')
    expect(within(table).getAllByRole('columnheader')).toHaveLength(50)
    expect(screen.getByText(/first 50 columns/)).toBeInTheDocument()
  })

  it('reads a tsv with tabs', async () => {
    serve('a\tb,c\n1\t2')
    show(csv({ name: 'd.tsv', content_type: 'text/tab-separated-values' }))
    const table = await screen.findByTestId('artifact-table')
    expect(within(table).getAllByRole('columnheader').map(c => c.textContent)).toEqual(['a', 'b,c'])
  })
})

describe('ArtifactViewer: images', () => {
  it('shows an svg only through <img src=blob:>, never as markup', async () => {
    serve('<svg xmlns="http://www.w3.org/2000/svg"><script>window.__pwned = 1</script><circle r="4"/></svg>')
    const { container } = show(art({ name: 'logo.svg', view: 'image', content_type: 'image/svg+xml' }))
    const img = await screen.findByRole('img', { name: 'logo.svg' })
    expect(img).toHaveAttribute('src', 'blob:test-0')
    expect(blobs[0].type).toBe('image/svg+xml')
    expect(container.ownerDocument.querySelector('svg')).toBeNull()
    expect(container.ownerDocument.querySelector('script')).toBeNull()
    expect(container.ownerDocument.querySelector('iframe')).toBeNull()
  })

  it('gives a png its own type', async () => {
    serve(new Uint8Array([1, 2, 3]))
    show(art({ name: 'p.png', view: 'image', content_type: 'image/png' }))
    await screen.findByRole('img')
    expect(blobs[0].type).toBe('image/png')
  })

  it('does not trust a content type the allow-list does not know', async () => {
    serve(new Uint8Array([1, 2, 3]))
    show(art({ name: 'p.png', view: 'image', content_type: 'text/html' }))
    await screen.findByRole('img')
    expect(blobs[0].type).toBe('application/octet-stream')
  })

  it('revokes the object URL when it goes away', async () => {
    serve(new Uint8Array([1]))
    const { unmount } = show(art({ name: 'p.png', view: 'image', content_type: 'image/png' }))
    await screen.findByRole('img')
    unmount()
    expect(revoked).toContain('blob:test-0')
  })
})

describe('ArtifactViewer: pdf, audio, video', () => {
  it('shows a pdf in an iframe whose src is a blob URL typed application/pdf, with an open-in-new-tab link', async () => {
    serve(new Uint8Array([0x25, 0x50, 0x44, 0x46]))
    show(art({ name: 'r.pdf', view: 'pdf', content_type: 'text/html' /* never believed */ }))
    const frame = (await screen.findByTitle('r.pdf')) as HTMLIFrameElement
    expect(frame.tagName).toBe('IFRAME')
    expect(frame.getAttribute('src')).toBe('blob:test-0')
    expect(blobs[0].type).toBe('application/pdf')
    expect(frame.getAttribute('srcdoc')).toBeNull()
    const link = screen.getByRole('link', { name: /Open in new tab/ })
    expect(link).toHaveAttribute('href', 'blob:test-0')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link.getAttribute('rel')).toContain('noopener')
  })

  it('plays audio from a blob URL', async () => {
    serve(new Uint8Array([1]))
    const { container } = show(art({ name: 'a.mp3', view: 'audio', content_type: 'audio/mpeg' }))
    await waitFor(() => expect(container.ownerDocument.querySelector('audio')).not.toBeNull())
    const el = container.ownerDocument.querySelector('audio')!
    expect(el).toHaveAttribute('src', 'blob:test-0')
    expect(el).toHaveAttribute('controls')
    expect(blobs[0].type).toBe('audio/mpeg')
  })

  it('plays video from a blob URL', async () => {
    serve(new Uint8Array([1]))
    const { container } = show(art({ name: 'v.webm', view: 'video', content_type: 'video/webm' }))
    await waitFor(() => expect(container.ownerDocument.querySelector('video')).not.toBeNull())
    const el = container.ownerDocument.querySelector('video')!
    expect(el).toHaveAttribute('src', 'blob:test-0')
    expect(el).toHaveAttribute('controls')
  })
})

describe('ArtifactViewer: html', () => {
  it('runs in a sandboxed iframe with scripts only, behind a CSP that cuts the network', async () => {
    const page = '<!doctype html><html><body><h1>Dash</h1><script>document.title = "ran"</script></body></html>'
    serve(page)
    const { container } = show(art({ name: 'dash.html', view: 'html', content_type: 'text/html' }))
    const frame = (await screen.findByTitle('dash.html')) as HTMLIFrameElement
    expect(frame.tagName).toBe('IFRAME')
    const sandbox = (frame.getAttribute('sandbox') ?? '').split(/\s+/).filter(Boolean)
    expect(sandbox).toEqual(['allow-scripts'])
    expect(frame.getAttribute('sandbox')).not.toContain('allow-same-origin')
    expect(frame.hasAttribute('src')).toBe(false)
    const srcdoc = frame.getAttribute('srcdoc')!
    expect(srcdoc).toContain(`<meta http-equiv="Content-Security-Policy" content="${HTML_ARTIFACT_CSP}">`)
    expect(srcdoc.indexOf('Content-Security-Policy')).toBeLessThan(srcdoc.indexOf('<script>'))
    expect(srcdoc).toContain('<h1>Dash</h1>')
    // The page's own script is only ever a string in an attribute of the sandboxed frame:
    // nothing ran in this document.
    expect(container.ownerDocument.querySelector('script')).toBeNull()
    expect(container.ownerDocument.querySelector('h1')).toBeNull()
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined()
  })

  it('puts the CSP first even for a fragment with no doctype', async () => {
    serve('<script>x()</script>')
    show(art({ name: 'f.html', view: 'html', content_type: 'text/html' }))
    const frame = (await screen.findByTitle('f.html')) as HTMLIFrameElement
    expect(frame.getAttribute('srcdoc')!.startsWith('<meta http-equiv="Content-Security-Policy"')).toBe(true)
  })
})

describe('ArtifactViewer: no preview', () => {
  it.each(['none', 'hologram', ''])('a %j view is download-only and is not fetched', view => {
    show(art({ name: 'memo.docx', view, size: 245760, content_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' }))
    const box = screen.getByTestId('artifact-no-preview')
    expect(box).toHaveTextContent('No preview for this file type — download it')
    expect(box).toHaveTextContent('memo.docx')
    expect(box).toHaveTextContent('240 KB')
    expect(box).toHaveTextContent('wordprocessingml.document')
    expect(screen.getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(apiFetch).not.toHaveBeenCalled()
  })
})

describe('ArtifactViewer: actions', () => {
  it('Download fetches the download route (through apiFetch, so with the bearer) and saves the blob', async () => {
    apiFetch.mockImplementation((url: string) => Promise.resolve(new Response(url.endsWith('/raw') ? 'shown' : 'file bytes')))
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    show(art({}))
    await screen.findByTestId('artifact-text')
    fireEvent.click(screen.getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(click).toHaveBeenCalled())
    expect(apiFetch).toHaveBeenCalledWith('/api/sessions/s1/artifacts/a1/download')
  })

  it('reports a download that fails', async () => {
    apiFetch.mockImplementation((url: string) => Promise.resolve(new Response(url.endsWith('/raw') ? 'shown' : '{}', { status: url.endsWith('/raw') ? 200 : 404 })))
    show(art({}))
    await screen.findByTestId('artifact-text')
    fireEvent.click(screen.getByRole('button', { name: 'Download' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('no longer available')
  })

  it('closes on Escape and on the close button', async () => {
    serve('x')
    const { onClose } = show(art({}))
    await screen.findByTestId('artifact-text')
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  it('names the file, its size and its kind in the header', async () => {
    serve('x')
    show(art({ name: 'notes.txt', size: 1536 }))
    const dialog = screen.getByRole('dialog', { name: /notes\.txt/ })
    expect(dialog).toHaveTextContent('1.5 KB')
    expect(dialog).toHaveTextContent('Text')
  })
})

describe('ArtifactViewer: versions', () => {
  const v2 = art({ id: 'b2', name: 'report.md', view: 'markdown', content_type: 'text/markdown', version: 2, latest_version: 3 })
  const v3 = art({ id: 'b3', name: 'report.md', view: 'markdown', content_type: 'text/markdown', version: 3, latest_version: 3 })

  it('titles a file with several versions "name · vN of M"', async () => {
    serve('# hi')
    render(<ArtifactViewer sessionId="s1" artifact={v3} onClose={vi.fn()} />)
    expect(await screen.findByRole('heading', { name: 'report.md · v3 of 3' })).toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: 'report.md · v3 of 3' })).toBeInTheDocument()
    expect(screen.queryByText(/older version/i)).toBeNull()
  })

  it('has the plain name for a file with a single version', async () => {
    serve('x')
    show(art({ version: 1, latest_version: 1 }))
    expect(await screen.findByRole('heading', { name: 'notes.txt' })).toBeInTheDocument()
  })

  it('has the plain name for a file the server sent no version for', async () => {
    serve('x')
    show(art({ id: 'z', name: 'bare.txt' }))
    expect(await screen.findByRole('heading', { name: 'bare.txt' })).toBeInTheDocument()
  })

  it('says it is an older version and offers View latest', async () => {
    serve('# old')
    const open = vi.fn()
    const latest = { ...v3 }
    render(<ArtifactViewer sessionId="s1" artifact={v2} latest={latest} onOpen={open} onClose={vi.fn()} />)
    expect(await screen.findByRole('heading', { name: 'report.md · v2 of 3' })).toBeInTheDocument()
    const note = screen.getByTestId('artifact-older')
    expect(note).toHaveTextContent(/older version/i)
    fireEvent.click(within(note).getByRole('button', { name: 'View latest' }))
    expect(open).toHaveBeenCalledWith(latest)
  })

  it('offers no View latest without the latest version at hand', async () => {
    serve('# old')
    render(<ArtifactViewer sessionId="s1" artifact={v2} onClose={vi.fn()} />)
    expect(await screen.findByTestId('artifact-older')).toHaveTextContent(/older version/i)
    expect(screen.queryByRole('button', { name: 'View latest' })).toBeNull()
  })

  it('downloads an older version under its -vN name', async () => {
    serve('# old')
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    render(<ArtifactViewer sessionId="s1" artifact={v2} onClose={vi.fn()} />)
    await screen.findByRole('heading', { name: 'report.md · v2 of 3' })
    serve('bytes')
    fireEvent.click(screen.getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(click).toHaveBeenCalled())
    expect((click.mock.contexts.at(-1) as HTMLAnchorElement).getAttribute('download')).toBe('report-v2.md')
  })
})
