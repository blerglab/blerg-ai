import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { ev, meta, resetChatState, seed, SESSION } from '../test/chat'
import { WorkspaceFileError } from '../transport/files'
import { resetImageQueueBackoff } from '../model/imageQueue'
import type { ArtifactInfo, WorkspaceFile } from '../transport/types'

beforeEach(() => { resetChatState(); resetImageQueueBackoff() })

const file = (over: Partial<WorkspaceFile> & { body?: string } = {}): WorkspaceFile => {
  const { body = '# Hello', ...rest } = over
  const blob = new Blob([body])
  return { path: 'README.md', size: blob.size, view: 'markdown', content_type: 'text/markdown; charset=utf-8', mtime: '2026-10-17T00:00:00Z', blob, ...rest }
}

interface Opts { can?: boolean; reply?: (path: string, signal?: AbortSignal) => Promise<WorkspaceFile>; published?: ArtifactInfo[] }

function mount(text: string, opts: Opts = {}) {
  seed(ev({ kind: 'assistant_text', payload: { text }, seq: 1 }))
  const t = createFakeTransport(opts.published ? { files: { items: opts.published } } : undefined)
  const read = vi.fn(async (_s: string, path: string, signal?: AbortSignal) => (opts.reply ? opts.reply(path, signal) : file({ path })))
  Object.assign(t.files!, { workspaceFile: read })
  render(<ChatView session={SESSION} transport={t.transport} meta={meta()} canOpenWorkspaceFiles={opts.can ?? true} />)
  return { ...t, read }
}

describe('files the agent names', () => {
  it.each(['/workspace/notes', '/workspace/bin/build'])('opens extensionless sandbox inline code %s intact', async path => {
    const { read } = mount(`Open \`${path}\`.`, { reply: async () => file({ path: path.slice('/workspace/'.length), view: 'text', body: 'sandbox file' }) })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const dialog = await screen.findByRole('dialog', { name: path })
    expect(await within(dialog).findByText('sandbox file')).toBeInTheDocument()
    expect(read).toHaveBeenCalledWith('s1', path, expect.anything())
  })

  it('opens a bare name the session published as that published file, not from the folder', async () => {
    const plan: ArtifactInfo = { id: 'p1', name: 'plan.md', size: 20, content_type: 'text/markdown', view: 'markdown', origin: 'agent' }
    const { read } = mount('Open `plan.md`, then `docs/plan.md`.', { published: [plan] })
    const bare = await screen.findByText('plan.md')
    fireEvent.click(bare.closest('[data-workspace-path]')!)
    expect(await screen.findByRole('dialog', { name: /plan\.md/ })).toBeInTheDocument()
    expect(read).not.toHaveBeenCalled()
  })

  it('still reads a bare name from the folder when nothing of that name is published', async () => {
    const { read } = mount('Open `notes.md`.', { published: [] })
    fireEvent.click((await screen.findByText('notes.md')).closest('[data-workspace-path]')!)
    await screen.findByRole('dialog', { name: 'notes.md' })
    expect(read).toHaveBeenCalledWith('s1', 'notes.md', expect.anything())
  })

  it.each(['/workspace/app/README.md', '/srv/projects/app/README.md'])('opens %s intact and uses the returned relative path for document links', async path => {
    const { read } = mount(`[Project README](${path})`, { reply: async p => {
      if (p === path) return file({ body: '# Project README\n\n[Guide](docs/guide.md)\n' })
      if (p === 'docs/guide.md') return file({ path: p, body: '# Guide' })
      throw new WorkspaceFileError(404, 'The workspace file was not found.')
    } })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const dialog = await screen.findByRole('dialog', { name: path })
    expect(read).toHaveBeenCalledWith('s1', path, expect.anything())
    expect(await within(dialog).findByRole('heading', { name: 'Project README', level: 1 })).toBeInTheDocument()
    fireEvent.click(await within(dialog).findByText('Guide'))
    expect(await screen.findByRole('heading', { name: 'Guide', level: 1 })).toBeInTheDocument()
    expect(read).toHaveBeenLastCalledWith('s1', 'docs/guide.md', expect.anything())
  })

  it('turns relative links and path-like inline code into buttons, and nothing else', async () => {
    mount('See [the readme](README.md#L3), `src/a.go:12`, `fmt.Println`, `1.2.3`, [web](https://x.io/a.md), ```` and\n\n```\nsrc/b.go\n```\n')
    const found = await screen.findAllByTitle('Open from the session’s folder')
    expect(found.map(e => e.getAttribute('data-workspace-path'))).toEqual(['README.md', 'src/a.go'])
    expect(screen.getByText('fmt.Println').closest('[data-workspace-path]')).toBeNull()
    expect(screen.getByText('src/b.go').closest('[data-workspace-path]')).toBeNull()
    expect(screen.getByRole('link', { name: 'web' })).toBeInTheDocument()
  })

  it('does nothing without the host opting in (and never for the owner-less)', async () => {
    mount('`README.md` and [x](docs/a.md)', { can: false })
    await screen.findByText('README.md')
    expect(document.querySelector('[data-workspace-path]')).toBeNull()
    expect(screen.queryByTitle('Open from the session’s folder')).toBeNull()
  })

  it('leaves the person’s own messages alone', async () => {
    seed(ev({ kind: 'user_message', payload: { text: 'look at `README.md` and [x](a.md)', source: 'user' }, seq: 1 }))
    const t = createFakeTransport()
    Object.assign(t.files!, { workspaceFile: vi.fn() })
    render(<ChatView session={SESSION} transport={t.transport} meta={meta()} canOpenWorkspaceFiles />)
    await screen.findByText(/look at/)
    expect(document.querySelector('[data-workspace-path]')).toBeNull()
  })

  it('opens the stripped path in the viewer, with its header and actions', async () => {
    const { read } = mount('`./docs/a.md:7`')
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const dialog = await screen.findByRole('dialog', { name: 'docs/a.md' })
    expect(read).toHaveBeenCalledWith('s1', 'docs/a.md', expect.anything())
    expect(within(dialog).getByText(/From the session’s folder · as of now/)).toBeInTheDocument()
    expect(await within(dialog).findByRole('heading', { level: 1, name: 'Hello' })).toBeInTheDocument()
    for (const b of ['Copy path', 'Download', 'Save a copy to Files']) expect(within(dialog).getByRole('button', { name: b })).toBeInTheDocument()
  })

  it('shows a code file as numbered lines and marks the line it was sent to', async () => {
    mount('`a.go:3`', { reply: async path => file({ path, view: 'text', body: 'package a\n\nfunc f() {}\nvar x = 1\n' }) })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const code = await screen.findByTestId('workspace-code')
    expect(code.querySelector('.code-viewer-gutter')?.textContent).toMatch(/^1\n2\n3\n4/)
    expect(await screen.findByTestId('workspace-line')).toBeInTheDocument()
  })

  it('draws html in a frame with no same-origin and no network, never in the page', async () => {
    mount('`page.html`', { reply: async path => file({ path, view: 'html', body: '<script>window.top.x=1</script><p id="p">hi</p>' }) })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const frame = await waitFor(() => {
      const f = document.querySelector('iframe.artifact-html')
      expect(f).not.toBeNull()
      return f as HTMLIFrameElement
    })
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
    expect(frame.getAttribute('srcdoc')).toMatch(/Content-Security-Policy/)
    expect(document.getElementById('p')).toBeNull()
  })

  it('draws svg only through <img> on a data: URL, never a same-origin blob: one', async () => {
    const create = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:x')
    mount('`logo.svg`', { reply: async path => file({ path, view: 'image', content_type: 'image/svg+xml', body: '<svg onload="x()"></svg>' }) })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const img = await screen.findByRole('img', { name: 'logo.svg' })
    expect(img.getAttribute('src')).toMatch(/^data:image\/svg\+xml;base64,/)
    expect(document.querySelector('svg[onload]')).toBeNull()
    expect(create).not.toHaveBeenCalled()
    create.mockRestore()
  })

  it('on a timeout still says what the server said, not only that it took too long', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      mount('`README.md`', { reply: async () => { throw new WorkspaceFileError(409, 'The workspace file reader is busy.', 'err-busy', 'busy') } })
      fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
      await vi.advanceTimersByTimeAsync(25_000)
      const box = await screen.findByTestId('workspace-error')
      expect(box.textContent).toContain('The file reader is busy')
      expect(box.textContent).not.toContain('took too long')
    } finally {
      vi.useRealTimers()
    }
  })

  it('waits out a busy reader (409 busy) and opens the file', async () => {
    let calls = 0
    mount('`README.md`', { reply: async path => {
      if (++calls === 1) throw new WorkspaceFileError(409, 'The workspace file reader is busy.', 'err-busy', 'busy')
      return file({ path, body: '# Opened after the wait' })
    } })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    expect(await screen.findByRole('heading', { name: 'Opened after the wait' }, { timeout: 5000 })).toBeInTheDocument()
    expect(calls).toBe(2)
  })

  it.each([
    [404, 'The workspace file was not found.', 'Not found in the session’s folder'],
    [403, 'Blerg doesn\'t open files that usually hold secrets (restricted filename).', 'Blerg doesn’t open files that usually hold secrets'],
    [403, 'Blerg could not safely open that workspace file.', 'Blerg can’t open that path'],
    [409, 'The session has no live workspace daemon.', 'The session isn’t running, so its files can’t be read'],
    [413, 'The workspace file is larger than its configured size limit.', 'Too large to open here'],
  ])('maps %i to a sentence of its own, with the error id and not the raw text', async (status, text, want) => {
    mount('`README.md`', { reply: async () => { throw new WorkspaceFileError(status, text, 'err-77') } })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const box = await screen.findByTestId('workspace-error')
    expect(box.textContent).toContain(want)
    expect(box.textContent).toContain('error id err-77')
    expect(box.textContent).not.toContain(text)
  })

  it.each([
    ['desktop_ownership', 'Your desktops'],
    ['session_ownership', 'Ask the session owner'],
    ['person_required', 'Sign in as a person'],
  ])('shows actionable %s guidance in the file viewer', async (code, action) => {
    mount('`.agent-notes/report.md`', { reply: async () => { throw new WorkspaceFileError(403, 'private internal detail', 'ownership-77', code) } })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const box = await screen.findByTestId('workspace-error')
    expect(box).toHaveTextContent(action)
    expect(box).toHaveTextContent('ownership-77')
    expect(box).not.toHaveTextContent('Not found')
    expect(box).not.toHaveTextContent('private internal detail')
  })

  it('shows a spinner with Cancel while it waits', async () => {
    let seen: AbortSignal | undefined
    mount('`README.md`', { reply: (_p, signal) => { seen = signal; return new Promise(() => {}) } })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const spin = await screen.findByTestId('workspace-loading')
    fireEvent.click(within(spin).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(seen?.aborted).toBe(true)
  })

  it('saves a copy to Files as the person’s own upload', async () => {
    const { files } = mount('`notes/a.txt`', { reply: async path => file({ path, view: 'text', body: 'kept' }) })
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Save a copy to Files' }))
    expect(await within(dialog).findByText('Saved a copy to Files.')).toBeInTheDocument()
    expect(files!.uploads).toHaveLength(1)
    expect(files!.uploads[0].file.name).toBe('a.txt')
    expect(await files!.uploads[0].file.text()).toBe('kept')
  })

  it('renders a folder README’s HTML: its images from the folder beside it, its links to the folder’s files', async () => {
    const realCreate = URL.createObjectURL
    URL.createObjectURL = vi.fn(() => 'blob:http://localhost/banner')
    try {
      const readme = '<p align="center"><img src="brand/banner.png" alt="Banner" width="100%"></p>\n\nSee <a href="AGENTS.md">AGENTS.md</a>.\n'
      const plan: ArtifactInfo = { id: 'p1', name: 'AGENTS.md', size: 20, content_type: 'text/markdown', view: 'markdown', origin: 'agent' }
      const { read } = mount('Open `docs/README.md`.', {
        published: [plan],
        reply: async path => path === 'docs/brand/banner.png'
          ? file({ path, view: 'image', content_type: 'image/png', body: 'png' })
          : file({ path, body: path === 'docs/README.md' ? readme : '# Agents' }),
      })
      fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
      const dialog = await screen.findByRole('dialog', { name: /docs\/README\.md/ })
      const img = await within(dialog).findByAltText('Banner')
      expect(img.getAttribute('src')).toBe('blob:http://localhost/banner')
      expect(read.mock.calls.map(c => c[1])).toContain('docs/brand/banner.png')
      // A relative link opens the folder's file beside the README, not the published file of that name.
      fireEvent.click(within(dialog).getByText('AGENTS.md'))
      await waitFor(() => expect(read.mock.calls.map(c => c[1])).toContain('docs/AGENTS.md'))
    } finally {
      URL.createObjectURL = realCreate
    }
  })
})
