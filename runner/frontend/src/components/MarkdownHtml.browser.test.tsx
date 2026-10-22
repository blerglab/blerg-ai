import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ArtifactViewer, ChatContext, Markdown, ReviewMode, type ChatContextValue, type ReviewFile, type TransportFiles, type WorkspaceFile } from '@blerglab/chat'
import readme from '../../../../README.md?raw'
import '../index.css'
import '@blerglab/chat/blerg.css'

// The repository's own README, opened from the session's folder: its raw HTML (a centered banner,
// badges, a video table, a screenshot grid) renders as GitHub renders it, its images come from the
// folder through the workspace-file read, and the badges (remote images) wait for a click.
const shotsDir = (import.meta as unknown as { env: Record<string, string | undefined> }).env.VITE_MD_SHOTS_DIR

async function png(label: string): Promise<Blob> {
  const c = document.createElement('canvas')
  c.width = 640
  c.height = 200
  const g = c.getContext('2d')!
  g.fillStyle = '#2f6fd6'
  g.fillRect(0, 0, 640, 200)
  g.fillStyle = '#fff'
  g.font = '28px sans-serif'
  g.fillText(label, 24, 110)
  return new Promise(resolve => c.toBlob(b => resolve(b!), 'image/png'))
}

// The server's workspace-file route allows two reads at once (for everyone) and answers 409 busy
// past that; this fake does the same, and is also busy for the first few calls.
class Busy extends Error { status = 409; code = 'busy'; serverText = 'The workspace file reader is busy.' }

function folder(busyFirst = 0) {
  const reads: string[] = []
  const stats = { calls: 0, busy: 0, inFlight: 0, peak: 0 }
  const readWorkspace = vi.fn(async (path: string): Promise<WorkspaceFile> => {
    stats.calls++
    stats.inFlight++
    stats.peak = Math.max(stats.peak, stats.inFlight)
    try {
      await new Promise(r => setTimeout(r, 15))
      if (stats.inFlight > 2 || stats.calls <= busyFirst) { stats.busy++; throw new Busy('busy') }
      reads.push(path)
      const blob = await png(path)
      return { path, size: blob.size, view: 'image', content_type: 'image/png', mtime: '', blob }
    } finally {
      stats.inFlight--
    }
  })
  return { reads, readWorkspace, stats }
}

afterEach(async () => { await page.viewport(1280, 900) })

describe('a README with raw HTML (real browser)', () => {
  it('renders the banner from the folder, the tables and the badge placeholders', async () => {
    await page.viewport(1280, 900)
    const { reads, readWorkspace } = folder()
    const open = vi.fn()
    const value = { sessionId: 's', files: null, view: vi.fn(), byName: () => undefined, latestVersion: () => undefined, openWorkspaceFile: open } as unknown as ChatContextValue
    const view = render(
      <ChatContext.Provider value={value}>
        <div className="artifact-viewer" style={{ width: 1078, padding: 32 }}>
          <Markdown className="artifact-markdown md-doc" html text={readme} document={{ path: 'README.md', readWorkspace }} />
        </div>
      </ChatContext.Provider>,
    )
    const md = view.container.querySelector<HTMLElement>('.md')!
    // No raw tag shows as text.
    expect(md.textContent).not.toMatch(/<\/?(p|img|table|td|a|b|br|sub)\b/)

    // The banner: centered, read from brand/ in the folder, drawn from a blob at full width.
    const banner = await within(md).findByAltText(/^blerg\[ai\]: your coding agents/)
    await waitFor(() => expect(banner.getAttribute('src')).toMatch(/^blob:/))
    expect(banner.closest('p')?.getAttribute('align')).toBe('center')
    expect(banner.getAttribute('width')).toBe('100%')
    await waitFor(() => expect((banner as HTMLImageElement).naturalWidth).toBe(640))
    expect(Math.round(banner.getBoundingClientRect().width)).toBe(Math.round(md.getBoundingClientRect().width))
    await waitFor(() => {
      expect(reads).toContain('brand/blerg-ai-banner.png')
      expect(reads).toContain('docs/media/more-power.jpg')
      expect(reads).toContain('docs/media/ui/approval.jpg')
    }, { timeout: 10_000 })

    // The badges: remote images, never fetched by themselves; their alt text and Load image.
    for (const alt of ['Release', 'CI', 'License']) {
      const holder = within(md).getAllByTestId('md-img-placeholder').find(p => p.textContent?.startsWith(alt))
      expect(holder, alt).toBeTruthy()
      expect(within(holder!).getByRole('button', { name: 'Load image' })).toBeTruthy()
    }
    expect(Array.from(md.querySelectorAll('img')).every(i => i.getAttribute('src')?.startsWith('blob:'))).toBe(true)
    expect(performance.getEntriesByType('resource').some(e => /shields\.io|badge\.svg/.test(e.name))).toBe(false)

    // The video table and the screenshot grid.
    const cells = Array.from(md.querySelectorAll('td'))
    const video = cells.find(td => td.textContent?.includes('MORE POWER'))!
    expect(video.getAttribute('width')).toBe('50%')
    expect(getComputedStyle(video).textAlign).toBe('center') // a cell's align becomes text-align, as GFM's does
    expect(video.querySelector('a')?.getAttribute('href')).toBe('https://www.youtube.com/watch?v=NKZk2pgB_LA')
    expect(video.querySelector('a')?.getAttribute('rel')).toBe('noopener noreferrer')
    expect(cells.filter(td => td.getAttribute('width') === '33%')).toHaveLength(3)
    expect(md.querySelector('sub b')?.textContent).toBe('Agents start agents, with your approval.')

    // A relative link opens the folder's file in Blerg.
    await userEvent.click(within(md).getAllByText('AGENTS.md')[0])
    expect(open).toHaveBeenCalledWith('AGENTS.md', undefined, { folder: true })

    if (shotsDir) await page.screenshot({ path: `${shotsDir}/readme-1280.png` })
  })

  it('still resolves review anchors on text inside raw HTML', async () => {
    const text = '<p align="center">\n  <b>One place to run all your coding agents</b>: Claude Code and Codex.\n</p>\n\n<table><tr><td width="50%">Left cell words</td><td>Right</td></tr></table>\n\nA plain paragraph after.\n'
    const artifact = { id: 'h1', name: 'README.md', view: 'markdown', content_type: 'text/markdown', size: text.length, version: 1 }
    const quotes = ['run all your coding agents', 'Left cell words', 'plain paragraph']
    const review: ReviewFile = {
      version: 1, file: { name: artifact.name, artifactId: artifact.id },
      requests: quotes.map((quote, i) => ({ id: `r${i}`, text: `About ${quote}`, status: 'open' as const, createdAt: '2026-01-01T00:00:00Z', anchor: { kind: 'text' as const, quote, occurrence: 1 } })),
    }
    const value: ChatContextValue = { sessionId: 'html', files: null, view: () => {}, byName: () => undefined, latestVersion: () => undefined, reviewFor: async () => review, submitFeedback: async () => true }
    const view = render(<ChatContext.Provider value={value}><ReviewMode artifact={artifact} text={text} onClose={() => {}} /></ChatContext.Provider>)
    const pageEl = await view.findByTestId('review-page')
    await waitFor(() => expect(pageEl.querySelector('p[align="center"] b')).not.toBeNull())
    expect(pageEl.querySelector('td[width="50%"]')?.textContent).toBe('Left cell words')
    await waitFor(() => expect(view.getAllByRole('button', { name: 'Show passage' })).toHaveLength(quotes.length))
    for (const b of view.getAllByRole('button', { name: 'Show passage' })) expect(b).toBeEnabled()
    const ranges = [...(CSS.highlights.get('chat-document-comments') ?? [])] as Range[]
    expect(ranges.map(r => r.toString()).sort()).toEqual([...quotes].sort())
  })

  // #147: in the owner's README the MORE POWER thumbnail stayed its alt text while its neighbor
  // loaded. The daemon dropped that reply (its connection was busy writing the neighbor's), so the
  // server answered 409 "did not provide a workspace file" after its timeout, and the page gave up.
  it('loads the MORE POWER thumbnail when the daemon dropped its first reply', async () => {
    const { readWorkspace } = folder()
    let dropped = 0
    const flaky = vi.fn(async (path: string) => {
      if (path === 'docs/media/more-power.jpg' && dropped++ === 0) {
        await new Promise(r => setTimeout(r, 30))
        throw Object.assign(new Error('dropped'), { status: 409, code: 'daemon_timeout', serverText: 'The session daemon did not provide a workspace file.' })
      }
      return readWorkspace(path)
    })
    const view = render(<Markdown className="artifact-markdown md-doc md-box" html text={readme} document={{ path: 'README.md', readWorkspace: flaky }} />)
    const thumb = await within(view.container).findByAltText(/^Watch MORE POWER/, undefined, { timeout: 10_000 })
    await waitFor(() => expect(thumb.getAttribute('src')).toMatch(/^blob:/))
    expect(dropped).toBe(2)
    expect(within(view.container).queryByRole('button', { name: /^Retry/ })).toBeNull()
  }, 20_000)

  it('loads every folder image through a busy server: two reads at a time, retried on 409', async () => {
    const { reads, readWorkspace, stats } = folder(4)
    const view = render(<Markdown className="artifact-markdown md-doc" html text={readme} document={{ path: 'README.md', readWorkspace }} />)
    const md = view.container.querySelector<HTMLElement>('.md')!
    const relative = (readme.match(/<img [^>]*src="(?!https?:)[^"]+"/g) ?? []).length
    expect(relative).toBeGreaterThan(4)
    await waitFor(() => expect(Array.from(md.querySelectorAll('img')).filter(i => i.getAttribute('src')?.startsWith('blob:'))).toHaveLength(relative), { timeout: 20_000 })
    expect(stats.busy).toBeGreaterThan(0) // the server did say busy
    expect(stats.calls).toBe(relative + stats.busy) // every busy answer was retried, nothing else
    expect(stats.peak).toBeLessThanOrEqual(2) // and the page never asked for more than it allows
    expect(new Set(reads).size).toBe(relative)
  }, 30_000)

  // An SVG with a script, as an agent could write one into the folder. Inside <img> it never runs;
  // the URL it is drawn from must not run it as the app either if someone opens the image on its own.
  const EVIL_SVG = `<svg xmlns="http://www.w3.org/2000/svg" width="120" height="40" onload="let r; try { r = 'read:' + localStorage.getItem('blerg-probe') } catch (e) { r = 'blocked:' + e.name } parent.postMessage({ svgProbe: r, origin: String(location.origin) }, '*')"><rect width="120" height="40" fill="#2f6fd6"/></svg>`

  async function probe(url: string): Promise<{ svgProbe: string; origin: string }> {
    localStorage.setItem('blerg-probe', 'app-secret')
    const got = new Promise<{ svgProbe: string; origin: string }>(resolve => {
      const on = (e: MessageEvent) => { if (e.data && typeof e.data === 'object' && 'svgProbe' in e.data) { window.removeEventListener('message', on); resolve(e.data) } }
      window.addEventListener('message', on)
    })
    const frame = document.createElement('iframe')
    frame.src = url // as "Open image in new tab" would load it, minus the top-level navigation browsers refuse
    document.body.appendChild(frame)
    try {
      return await Promise.race([got, new Promise<never>((_, reject) => setTimeout(() => reject(new Error('no report')), 8000))])
    } finally {
      frame.remove()
      localStorage.removeItem('blerg-probe')
    }
  }

  it('draws a folder SVG from a data: URL that cannot reach the app', async () => {
    const readWorkspace = vi.fn(async (path: string): Promise<WorkspaceFile> => {
      const blob = new Blob([EVIL_SVG])
      return { path, size: blob.size, view: 'image', content_type: 'image/svg+xml', mtime: '', blob }
    })
    const view = render(<Markdown className="md-doc" html text={'<p align="center"><img src="brand/logo.svg" alt="Logo"></p>'} document={{ path: 'README.md', readWorkspace }} />)
    const img = await within(view.container).findByAltText('Logo') as HTMLImageElement
    expect(img.src.startsWith('data:image/svg+xml;base64,')).toBe(true) // never a same-origin blob:
    await waitFor(() => expect(img.naturalWidth).toBe(120)) // and it draws
    const opened = await probe(img.src)
    expect(opened.origin).toBe('null') // an opaque origin
    expect(opened.svgProbe).toMatch(/^blocked:/) // no app storage
  }, 20_000)

  it('the file viewer draws an SVG artifact from a data: URL too', async () => {
    const files: TransportFiles = {
      list: async () => [], raw: async () => new Blob([EVIL_SVG]), download: async () => {}, remove: async () => {},
      upload: async () => { throw new Error('unused') },
    }
    const view = render(<ArtifactViewer sessionId="s" files={files} artifact={{ id: 'svg1', name: 'logo.svg', size: EVIL_SVG.length, content_type: 'image/svg+xml', view: 'image' }} onClose={() => {}} />)
    const img = await within(view.baseElement).findByRole('img', { name: 'logo.svg' }) as HTMLImageElement
    await waitFor(() => expect(img.src.startsWith('data:image/svg+xml;base64,')).toBe(true))
    expect((await probe(img.src)).svgProbe).toMatch(/^blocked:/)
  }, 20_000)
})
