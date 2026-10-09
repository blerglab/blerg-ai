import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import ChatView from './ChatView'
import { buildTimeline, isVisibleEvent } from '../model/toolGroups'
import { createFakeTransport, type FakeTransport } from '../test/fakeTransport'
import { COMPACT, SESSION, file, meta, resetChatState, seed } from '../test/chat'
import type { ArtifactInfo } from '../transport/types'
import type { AgentEvent } from '../types'

const session = meta({ status: 'idle' })

const report: ArtifactInfo = { id: 'a1', name: 'report.md', size: 2048, content_type: 'text/markdown', view: 'markdown' }
const memo: ArtifactInfo = { id: 'a2', name: 'memo.docx', size: 245760, content_type: 'application/x', view: 'none' }

function artifactEvent(payload: ArtifactInfo, seq: number): AgentEvent {
  return {
    type: 'agent_event', session_id: SESSION, client_event_id: `e${seq}`, seq, ts: '2026-07-26T00:00:00Z',
    kind: 'artifact', payload,
  }
}

function mount(opts: { listed?: ArtifactInfo[]; linked?: string | null; onConsumed?: () => void; files?: false } = {}) {
  const t = createFakeTransport({ files: opts.files === false ? false : { items: opts.listed ?? [] } })
  t.files?.serve('# Report\n\nall good')
  const view = (linked: string | null | undefined) => (
    <ChatView session={SESSION} transport={t.transport} meta={session} linkedArtifact={linked} onLinkedArtifactConsumed={opts.onConsumed} />
  )
  const r = render(view(opts.linked))
  return { t, files: t.files!, ...r, relink: (linked: string | null) => r.rerender(view(linked)) }
}

beforeEach(() => {
  resetChatState()
  Object.assign(URL, { createObjectURL: vi.fn(() => 'blob:x'), revokeObjectURL: vi.fn() })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('artifact cards in the chat', () => {
  it('draws a card: name, size, Download and (for a viewable type) View', async () => {
    seed(artifactEvent(report, 1))
    mount()
    const card = await screen.findByTestId('artifact-card')
    expect(card).toHaveTextContent('📎')
    expect(card).toHaveTextContent('report.md')
    expect(card).toHaveTextContent('2.0 KB')
    expect(within(card).getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'View' })).toBeInTheDocument()
  })

  it('has no View for a download-only file', async () => {
    seed(artifactEvent(memo, 1))
    mount()
    const card = await screen.findByTestId('artifact-card')
    expect(within(card).getByRole('button', { name: 'Download' })).toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'View' })).toBeNull()
  })

  it('draws a card in the full (not compact) view too', async () => {
    localStorage.setItem(COMPACT, '0')
    seed(artifactEvent(report, 1))
    mount()
    expect(await screen.findByTestId('artifact-card')).toBeInTheDocument()
    localStorage.removeItem(COMPACT)
  })

  it('View opens the viewer on that file, read through the transport', async () => {
    seed(artifactEvent(report, 1))
    const { files } = mount()
    const card = await screen.findByTestId('artifact-card')
    fireEvent.click(within(card).getByRole('button', { name: 'View' }))
    const dialog = await screen.findByRole('dialog', { name: /report\.md/ })
    expect(await within(dialog).findByRole('heading', { name: 'Report' })).toBeInTheDocument()
    expect(files.rawCalls).toEqual(['a1'])
  })

  it('Download goes through the transport', async () => {
    seed(artifactEvent(report, 1))
    const { files } = mount()
    const card = await screen.findByTestId('artifact-card')
    fireEvent.click(within(card).getByRole('button', { name: 'Download' }))
    await waitFor(() => expect(files.downloads).toEqual(['a1']))
    expect(within(card).queryByRole('alert')).toBeNull()
  })

  it('a file deleted since says so instead of failing silently', async () => {
    seed(artifactEvent(report, 1))
    const { files } = mount()
    files.downloadError = new Error('This file is no longer available.')
    const card = await screen.findByTestId('artifact-card')
    fireEvent.click(within(card).getByRole('button', { name: 'Download' }))
    expect(await within(card).findByRole('alert')).toHaveTextContent('no longer available')
  })

  it('is a card kind, so it shows up in the compact timeline', () => {
    const ev = artifactEvent(report, 1)
    expect(isVisibleEvent(ev)).toBe(true)
    expect(buildTimeline([ev])).toEqual([{ type: 'event', ev }])
  })

  it('R4: without file operations on the transport, a card still names the file but offers nothing', async () => {
    seed(artifactEvent(report, 1))
    const { t } = mount({ files: false })
    expect(t.transport.files).toBeUndefined()
    const card = await screen.findByTestId('artifact-card')
    expect(card).toHaveTextContent('report.md')
    expect(card).toHaveTextContent('2.0 KB')
    expect(within(card).queryByRole('button', { name: 'Download' })).toBeNull()
    expect(within(card).queryByRole('button', { name: 'View' })).toBeNull()
    expect(screen.queryByTestId('files-toggle')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Attach files' })).toBeNull()
    expect(screen.queryByTestId('attach-input')).toBeNull()
    // Dropping a file does nothing: no drop zone, no chip.
    const chat = screen.getByTestId('agent-chat')
    fireEvent.dragEnter(chat, { dataTransfer: { types: ['Files'], files: [] } })
    expect(screen.queryByTestId('drop-zone')).toBeNull()
    fireEvent.drop(chat, { dataTransfer: { types: ['Files'], files: [file('dropped.pdf')] } })
    expect(screen.queryAllByTestId('attach-chip')).toHaveLength(0)
    // Nor does pasting one.
    fireEvent.paste(screen.getByRole('textbox'), { clipboardData: { files: [file('shot.png')], types: ['Files'], getData: () => '' } })
    expect(screen.queryAllByTestId('attach-chip')).toHaveLength(0)
  })
})

describe('the Files panel', () => {
  it('counts the session’s files on the toolbar button', async () => {
    mount({ listed: [report, memo] })
    const btn = await screen.findByRole('button', { name: /Files \(2\)/ })
    expect(btn).toBeInTheDocument()
  })

  it('shows Files (0) and an empty state when there are none', async () => {
    mount()
    fireEvent.click(await screen.findByRole('button', { name: /Files \(0\)/ }))
    expect(await screen.findByTestId('artifacts-empty')).toBeInTheDocument()
  })

  it('says how the agent publishes in the host’s words', async () => {
    const t = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={session} publishHint="myapp publish <file>" />)
    fireEvent.click(await screen.findByRole('button', { name: /Files \(0\)/ }))
    expect(await screen.findByTestId('artifacts-empty')).toHaveTextContent('myapp publish <file>')
  })

  it('opens a list, newest first, and opens a file from it', async () => {
    mount({ listed: [report, memo] })
    fireEvent.click(await screen.findByRole('button', { name: /Files \(2\)/ }))
    const panel = await screen.findByRole('dialog', { name: /Files/ })
    expect(within(panel).getAllByTestId('artifact-name').map(n => n.textContent)).toEqual(['report.md', 'memo.docx'])
    fireEvent.click(within(within(panel).getAllByTestId('artifact-row')[0]).getByRole('button', { name: 'View' }))
    expect(await screen.findByRole('dialog', { name: /report\.md/ })).toBeInTheDocument()
  })

  it('reloads the list when a new artifact event arrives', async () => {
    const { t, files } = mount()
    await screen.findByRole('button', { name: /Files \(0\)/ })
    files.items = [report]
    act(() => t.emit(artifactEvent(report, 1)))
    expect(await screen.findByRole('button', { name: /Files \(1\)/ })).toBeInTheDocument()
  })

  it('deleting from the panel goes through the transport and reads the list again', async () => {
    const { files } = mount({ listed: [report] })
    fireEvent.click(await screen.findByRole('button', { name: /Files \(1\)/ }))
    const panel = await screen.findByRole('dialog', { name: /Files/ })
    fireEvent.click(within(panel).getByRole('button', { name: 'Delete' }))
    fireEvent.click(within(panel).getByRole('button', { name: 'Yes, delete' }))
    await waitFor(() => expect(files.removed).toEqual(['a1']))
    expect(await screen.findByTestId('artifacts-empty')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Files \(0\)/ })).toBeInTheDocument()
  })
})

describe('the linked artifact (the ?artifact= link the runner’s CLI prints)', () => {
  it('opens that artifact as soon as the list is read', async () => {
    const { files } = mount({ listed: [report], linked: 'a1' })
    const dialog = await screen.findByRole('dialog', { name: /report\.md/ })
    expect(await within(dialog).findByRole('heading', { name: 'Report' })).toBeInTheDocument()
    expect(files.rawCalls).toEqual(['a1'])
  })

  it('tells the host once the viewer is closed, so it can take the link out of its address', async () => {
    const onConsumed = vi.fn()
    mount({ listed: [report], linked: 'a1', onConsumed })
    const dialog = await screen.findByRole('dialog', { name: /report\.md/ })
    expect(onConsumed).not.toHaveBeenCalled()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(onConsumed).toHaveBeenCalledTimes(1)
  })

  it('says so when the artifact no longer exists, and Dismiss consumes the link', async () => {
    const onConsumed = vi.fn()
    mount({ listed: [report], linked: 'gone', onConsumed })
    expect(await screen.findByTestId('artifact-missing')).toHaveTextContent(/no longer exists/)
    expect(screen.queryByRole('dialog')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByTestId('artifact-missing')).toBeNull()
    expect(onConsumed).toHaveBeenCalledTimes(1)
  })

  it('does nothing without the prop', async () => {
    mount({ listed: [report] })
    await screen.findByRole('button', { name: /Files \(1\)/ })
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('follows a new link the host hands it later', async () => {
    const { relink } = mount({ listed: [report, memo] })
    await screen.findByRole('button', { name: /Files \(2\)/ })
    expect(screen.queryByRole('dialog')).toBeNull()
    relink('a1')
    expect(await screen.findByRole('dialog', { name: /report\.md/ })).toBeInTheDocument()
  })
})

describe('versions in the chat', () => {
  const v = (id: string, version: number, latest = 3): ArtifactInfo => ({ ...report, id, version, latest_version: latest, created_at: `2026-09-30T1${version}:00:00Z` })

  it('puts a version badge on the card of a later version', async () => {
    seed(artifactEvent({ ...report, version: 3 }, 1))
    mount()
    const card = await screen.findByTestId('artifact-card')
    expect(card).toHaveTextContent('report.md · v3 · 2.0 KB')
  })

  it('leaves the badge off a version 1 whose name has no other versions', async () => {
    seed(artifactEvent({ ...report, version: 1 }, 1))
    mount({ listed: [{ ...report, version: 1, latest_version: 1 }] })
    const card = await screen.findByTestId('artifact-card')
    await screen.findByRole('button', { name: /Files \(1\)/ })
    expect(card).not.toHaveTextContent(/\bv1\b/)
    expect(card.querySelector('.artifact-card-version')).toBeNull()
  })

  it('shows the badge on a version 1 card once its file has later versions', async () => {
    seed(artifactEvent({ ...report, version: 1 }, 1))
    mount({ listed: [v('a2', 2, 2), { ...report, version: 1, latest_version: 2 }] })
    const card = await screen.findByTestId('artifact-card')
    await waitFor(() => expect(card).toHaveTextContent('report.md · v1 · 2.0 KB'))
  })

  it('the toolbar counts files, not versions, and the viewer of an older version links to the latest', async () => {
    const { files } = mount({ listed: [v('a3', 3), v('a2', 2), v('a1', 1)], linked: 'a2' })
    expect(await screen.findByRole('button', { name: /Files \(1\)/ })).toBeInTheDocument()
    const dialog = await screen.findByRole('dialog', { name: 'report.md · v2 of 3' })
    expect(within(dialog).getByTestId('artifact-older')).toBeInTheDocument()
    expect(files.rawCalls).toContain('a2')
    fireEvent.click(within(dialog).getByRole('button', { name: 'View latest' }))
    expect(await screen.findByRole('dialog', { name: 'report.md · v3 of 3' })).toBeInTheDocument()
    await waitFor(() => expect(files.rawCalls).toContain('a3'))
  })

  it('the linked artifact works for the latest version too', async () => {
    mount({ listed: [v('a3', 3), v('a2', 2)], linked: 'a3' })
    expect(await screen.findByRole('dialog', { name: 'report.md · v3 of 3' })).toBeInTheDocument()
    expect(screen.queryByTestId('artifact-older')).toBeNull()
  })
})

describe('inline previews', () => {
  const shot: ArtifactInfo = { id: 'p1', name: 'shot.png', size: 12 * 1024, content_type: 'image/png', view: 'image' }
  const clip: ArtifactInfo = { id: 'v1', name: 'demo.mp4', size: 3 * 1024 * 1024, content_type: 'video/mp4', view: 'video' }
  const huge: ArtifactInfo = { id: 'p2', name: 'poster.png', size: 11 * 1024 * 1024, content_type: 'image/png', view: 'image' }

  it('draws an image in its card, fetched once through the transport, and opens the viewer on click', async () => {
    seed(artifactEvent(shot, 1))
    const { files } = mount()
    const preview = await screen.findByTestId('artifact-preview')
    const img = await within(preview).findByRole('img', { name: 'shot.png' })
    expect(img).toHaveAttribute('src', 'blob:x')
    expect(files.rawCalls).toEqual(['p1'])
    fireEvent.click(preview)
    const dialog = await screen.findByRole('dialog', { name: /shot\.png/ })
    expect(dialog).toBeInTheDocument()
  })

  it('draws a play tile for a video without fetching it, and the viewer autoplays it on click', async () => {
    seed(artifactEvent(clip, 1))
    const { files } = mount()
    const tile = await screen.findByTestId('artifact-preview-tile')
    expect(tile).toHaveTextContent('demo.mp4')
    expect(files.rawCalls).toHaveLength(0)
    fireEvent.click(tile)
    const dialog = await screen.findByRole('dialog', { name: /demo\.mp4/ })
    await waitFor(() => expect(dialog.querySelector('video')).not.toBeNull())
    expect(dialog.querySelector('video')).toHaveAttribute('autoplay')
  })

  it('View (not the preview) opens a video without autoplay', async () => {
    seed(artifactEvent(clip, 1))
    mount()
    const card = await screen.findByTestId('artifact-card')
    fireEvent.click(within(card).getByRole('button', { name: 'View' }))
    const dialog = await screen.findByRole('dialog', { name: /demo\.mp4/ })
    await waitFor(() => expect(dialog.querySelector('video')).not.toBeNull())
    expect(dialog.querySelector('video')).not.toHaveAttribute('autoplay')
  })

  it('draws an HTML page small in its card, sandboxed and out of reach, and opens the viewer on click', async () => {
    const page: ArtifactInfo = { id: 'h1', name: 'mockup.html', size: 4096, content_type: 'text/html', view: 'html' }
    seed(artifactEvent(page, 1))
    const { files } = mount()
    const box = await screen.findByTestId('artifact-preview-html')
    const frame = await waitFor(() => {
      const f = box.querySelector('iframe')
      expect(f).not.toBeNull()
      return f!
    })
    // Its own origin, scripts only: no allow-same-origin, no popups, no top navigation, no forms.
    expect(frame.getAttribute('sandbox')).toBe('allow-scripts')
    // The no-network CSP comes before anything the page can run.
    const src = frame.getAttribute('srcdoc')!
    expect(src.startsWith('<meta http-equiv="Content-Security-Policy"')).toBe(true)
    expect(src).toContain("default-src 'none'")
    // Not reachable from the transcript: no focus, no tab stop, nothing for a screen reader.
    expect(frame.getAttribute('tabindex')).toBe('-1')
    expect(frame.getAttribute('aria-hidden')).toBe('true')
    expect(frame.hasAttribute('inert')).toBe(true)
    expect(files.rawCalls).toEqual(['h1'])

    fireEvent.click(within(box).getByRole('button', { name: 'Open mockup.html' }))
    const dialog = await screen.findByRole('dialog', { name: /mockup\.html/ })
    await waitFor(() => expect(dialog.querySelector('iframe')).not.toBeNull())
    expect(dialog.querySelector('iframe')!.getAttribute('sandbox')).toBe('allow-scripts')
  })

  it('keeps the plain card for an HTML page too large to render', async () => {
    const big: ArtifactInfo = { id: 'h2', name: 'big.html', size: 3 * 1024 * 1024, content_type: 'text/html', view: 'html' }
    seed(artifactEvent(big, 1))
    const { files } = mount()
    const card = await screen.findByTestId('artifact-card')
    expect(within(card).queryByTestId('artifact-preview-html')).toBeNull()
    expect(files.rawCalls).toHaveLength(0)
  })

  it('keeps the plain card for an image over the inline limit', async () => {
    seed(artifactEvent(huge, 1))
    const { files } = mount()
    const card = await screen.findByTestId('artifact-card')
    expect(within(card).queryByTestId('artifact-preview')).toBeNull()
    expect(within(card).getByRole('button', { name: 'View' })).toBeInTheDocument()
    expect(files.rawCalls).toHaveLength(0)
  })

  it('shows an attached image inline under the message, matched by name to the Files list', async () => {
    seed({
      type: 'agent_event', session_id: SESSION, client_event_id: 'e1', seq: 1, ts: '2026-07-26T00:00:00Z',
      kind: 'user_message', payload: { text: 'look\n\nAttached files (fetch them with: blerg-runner fetch --all): shot.png (12.0 KB)', source: 'human' },
    })
    const { files } = mount({ listed: [{ ...shot, id: 'u1', origin: 'user' }] })
    const previews = await screen.findByTestId('message-attachment-previews')
    expect(await within(previews).findByRole('img', { name: 'shot.png' })).toHaveAttribute('src', 'blob:x')
    expect(files.rawCalls).toEqual(['u1'])
    expect(screen.getByTestId('message-attachments')).toHaveTextContent('shot.png')
  })

  it('shows the chips but no preview when the transport has no file operations', async () => {
    seed({
      type: 'agent_event', session_id: SESSION, client_event_id: 'e1', seq: 1, ts: '2026-07-26T00:00:00Z',
      kind: 'user_message', payload: { text: 'look\n\nAttached files (fetch them with: blerg-runner fetch --all): shot.png (12.0 KB)', source: 'human' },
    })
    mount({ files: false })
    expect(await screen.findByTestId('message-attachments')).toHaveTextContent('shot.png')
    expect(screen.queryByTestId('message-attachment-previews')).toBeNull()
  })
})

describe('the Files panel and the attach control', () => {
  it('Attach files from the empty state opens the file picker and closes the panel', async () => {
    const t: FakeTransport = createFakeTransport()
    render(<ChatView session={SESSION} transport={t.transport} meta={session} />)
    fireEvent.click(await screen.findByRole('button', { name: /Files \(0\)/ }))
    const input = screen.getByTestId('attach-input') as HTMLInputElement
    const click = vi.spyOn(input, 'click')
    const panel = await screen.findByRole('dialog', { name: /Files/ })
    fireEvent.click(within(panel).getByRole('button', { name: 'Attach files' }))
    expect(click).toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
