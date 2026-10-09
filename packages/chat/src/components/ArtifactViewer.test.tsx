import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ArtifactViewer from './ArtifactViewer'
import { ChatContext, type ChatContextValue } from './context'
import { HTML_ARTIFACT_CSP } from '../model/artifacts'
import { createFakeFiles, type FakeFiles } from '../test/fakeTransport'
import type { ArtifactInfo } from '../transport/types'

let blobs: Blob[]
let revoked: string[]
let nextUrl: number
let files: FakeFiles

function art(over: Partial<ArtifactInfo>): ArtifactInfo {
  return { id: 'a1', name: 'notes.txt', size: 100, content_type: 'text/plain; charset=utf-8', view: 'text', ...over }
}

function serve(body: string | Uint8Array, status = 200) {
  files.serve(body, status)
}

function show(a: ArtifactInfo, onClose = vi.fn()) {
  return { onClose, ...render(<ArtifactViewer sessionId="s1" files={files} artifact={a} onClose={onClose} />) }
}

/** A chat around the viewer, as ChatView provides: the feedback modes need its send path. */
function chatValue(): ChatContextValue {
  return {
    sessionId: 's1',
    files,
    view: vi.fn(),
    latestVersion: () => undefined,
    byName: () => undefined,
    submitFeedback: vi.fn(async () => true),
    reviewFor: vi.fn(async () => null),
  }
}

function showInChat(a: ArtifactInfo) {
  const chat = chatValue()
  return {
    chat,
    ...render(
      <ChatContext.Provider value={chat}>
        <ArtifactViewer sessionId="s1" files={files} artifact={a} onClose={vi.fn()} />
      </ChatContext.Provider>,
    ),
  }
}

beforeEach(() => {
  files = createFakeFiles()
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
  it('reads the bytes through the transport and shows the text as text, never as markup', async () => {
    serve('hello <b>world</b>\n<script>window.__pwned = 1</script>')
    const { container } = show(art({}))
    const pre = await screen.findByTestId('artifact-text')
    expect(pre.textContent).toContain('<script>window.__pwned = 1</script>')
    expect(files.rawCalls).toEqual(['a1'])
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
    expect(files.rawCalls).toHaveLength(0)
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

  it('autoplays a video opened from its preview', async () => {
    serve(new Uint8Array([1]))
    const { container } = render(<ArtifactViewer sessionId="s1" files={files} artifact={art({ name: 'v.webm', view: 'video', content_type: 'video/webm' })} onClose={vi.fn()} autoplay />)
    await waitFor(() => expect(container.ownerDocument.querySelector('video')).not.toBeNull())
    expect(container.ownerDocument.querySelector('video')).toHaveAttribute('autoplay')
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
    expect(files.rawCalls).toHaveLength(0)
  })
})

describe('ArtifactViewer: actions', () => {
  it('Download goes through the transport (which carries the credentials and saves the blob)', async () => {
    serve('shown')
    show(art({}))
    await screen.findByTestId('artifact-text')
    fireEvent.click(screen.getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(files.downloads).toEqual(['a1']))
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('reports a download that fails', async () => {
    serve('shown')
    files.downloadError = new Error('This file is no longer available.')
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

describe('ArtifactViewer: feedback modes', () => {
  const md = art({ name: 'r.md', view: 'markdown', content_type: 'text/markdown' })
  const pdf = art({ name: 'r.pdf', view: 'pdf', content_type: 'application/pdf' })
  const png = art({ name: 'p.png', view: 'image', content_type: 'image/png' })

  it('offers Review for a markdown file once it has loaded, and opens the review in place', async () => {
    serve('# Title\n\nsome text')
    showInChat(md)
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull() // not before the bytes are here
    await screen.findByRole('heading', { name: 'Title' })
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(screen.getByTestId('review-mode')).toBeInTheDocument()
    expect((screen.getByRole('textbox', { name: 'Source' }) as HTMLTextAreaElement).value).toBe('# Title\n\nsome text')
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Mark up' })).toBeNull()
    // Escape inside the review goes back to the file, not out of the dialog.
    fireEvent.keyDown(screen.getByTestId('review-mode'), { key: 'Escape' })
    expect(screen.queryByTestId('review-mode')).toBeNull()
    expect(screen.getByRole('heading', { name: 'Title' })).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('offers Review for a pdf and hands the review the bytes', async () => {
    serve(new Uint8Array([0x25, 0x50, 0x44, 0x46]))
    showInChat(pdf)
    await screen.findByTitle('r.pdf')
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    expect(screen.getByTestId('review-mode')).toBeInTheDocument()
    expect(screen.queryByRole('textbox', { name: 'Source' })).toBeNull()
    expect(screen.queryByTitle('r.pdf')).toBeNull()
  })

  it('offers Mark up for an image, and Back returns to the file', async () => {
    serve(new Uint8Array([1, 2, 3]))
    showInChat(png)
    await screen.findByRole('img', { name: 'p.png' })
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Mark up' }))
    expect(screen.queryByRole('button', { name: 'Mark up' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Back' })).toBeInTheDocument()
    expect(document.querySelector('.artifact-image')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(await screen.findByRole('img', { name: 'p.png' })).toHaveClass('artifact-image')
    expect(screen.getByRole('button', { name: 'Mark up' })).toBeInTheDocument()
  })

  it('offers neither for a text file', async () => {
    serve('plain')
    showInChat(art({}))
    await screen.findByTestId('artifact-text')
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Mark up' })).toBeNull()
  })

  it('offers neither without a chat around the viewer', async () => {
    serve('# Title')
    show(md)
    await screen.findByRole('heading', { name: 'Title' })
    expect(screen.queryByRole('button', { name: 'Review' })).toBeNull()
    serve(new Uint8Array([1]))
    show(png)
    await screen.findByRole('img', { name: 'p.png' })
    expect(screen.queryByRole('button', { name: 'Mark up' })).toBeNull()
  })
})

describe('ArtifactViewer: versions', () => {
  const v2 = art({ id: 'b2', name: 'report.md', view: 'markdown', content_type: 'text/markdown', version: 2, latest_version: 3 })
  const v3 = art({ id: 'b3', name: 'report.md', view: 'markdown', content_type: 'text/markdown', version: 3, latest_version: 3 })

  it('titles a file with several versions "name · vN of M"', async () => {
    serve('# hi')
    render(<ArtifactViewer sessionId="s1" files={files} artifact={v3} onClose={vi.fn()} />)
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
    render(<ArtifactViewer sessionId="s1" files={files} artifact={v2} latest={latest} onOpen={open} onClose={vi.fn()} />)
    expect(await screen.findByRole('heading', { name: 'report.md · v2 of 3' })).toBeInTheDocument()
    const note = screen.getByTestId('artifact-older')
    expect(note).toHaveTextContent(/older version/i)
    fireEvent.click(within(note).getByRole('button', { name: 'View latest' }))
    expect(open).toHaveBeenCalledWith(latest)
  })

  it('offers no View latest without the latest version at hand', async () => {
    serve('# old')
    render(<ArtifactViewer sessionId="s1" files={files} artifact={v2} onClose={vi.fn()} />)
    expect(await screen.findByTestId('artifact-older')).toHaveTextContent(/older version/i)
    expect(screen.queryByRole('button', { name: 'View latest' })).toBeNull()
  })

  it('downloads an older version by its own id (the transport names the file)', async () => {
    serve('# old')
    render(<ArtifactViewer sessionId="s1" files={files} artifact={v2} onClose={vi.fn()} />)
    await screen.findByRole('heading', { name: 'report.md · v2 of 3' })
    fireEvent.click(screen.getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(files.downloads).toEqual(['b2']))
  })
})
