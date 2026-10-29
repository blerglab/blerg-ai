import { StrictMode, useState } from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { expect, it, vi } from 'vitest'
import ArtifactViewer from './ArtifactViewer'
import WorkspaceFileViewer from './WorkspaceFileViewer'
import CommitViewer from './CommitViewer'
import { HostedViewers, ViewerHostContext, type ViewerHostValue, type HostedView } from './ViewerHost'
import ChatView from './ChatView'
import { ev, meta, resetChatState, seed, SESSION } from '../test/chat'
import { createFakeTransport, createFakeFiles } from '../test/fakeTransport'
import { ChatContext, type ChatContextValue } from './context'
const artifact = { id: 'a1', name: 'report.md', size: 12, view: 'markdown', content_type: 'text/markdown' }
function host(over: Partial<ViewerHostValue> = {}): ViewerHostValue { return { open: vi.fn(), close: vi.fn(), keep: vi.fn(), views: [], active: 'chat', target: null, ...over } }
it('hands artifact, workspace and commit requests to the host without opening modals or reading bytes', async () => {
  const value = host(), files = createFakeFiles(), consumed = vi.fn()
  const view = render(<StrictMode><ViewerHostContext.Provider value={value}><ArtifactViewer sessionId="s1" files={files} artifact={artifact} onClose={consumed} /></ViewerHostContext.Provider></StrictMode>)
  await waitFor(() => expect(value.open).toHaveBeenCalledWith(expect.objectContaining({ kind: 'artifact', artifact })))
  expect(consumed).toHaveBeenCalledOnce()
  expect(files.rawCalls).toEqual([])
  view.rerender(<ViewerHostContext.Provider value={value}><WorkspaceFileViewer sessionId="s1" files={files} path="src/app.ts" onClose={consumed} /><CommitViewer sessionId="s1" files={files} sha="abcdef12" onClose={consumed} /></ViewerHostContext.Provider>)
  await waitFor(() => expect(value.open).toHaveBeenCalledWith(expect.objectContaining({ kind: 'workspace', path: 'src/app.ts' })))
  expect(value.open).toHaveBeenCalledWith(expect.objectContaining({ kind: 'commit', sha: 'abcdef12' }))
  expect(screen.queryByRole('dialog')).toBeNull()
})
it('embeds the real renderer in a host target and retains chat feedback context and inactive views', async () => {
  const files = createFakeFiles(); files.serve('# Report')
  const target = document.createElement('div'); document.body.appendChild(target)
  const value = host({ target, active: 'report', views: [{ id: 'report', view: { kind: 'artifact', artifact } }] })
  const chat: ChatContextValue = { sessionId: 's1', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined, submitFeedback: vi.fn(async () => true), reviewFor: vi.fn(async () => null) }
  const tree = (active: string) => <ViewerHostContext.Provider value={{ ...value, active }}><ChatContext.Provider value={chat}><HostedViewers sessionId="s1" files={files} /></ChatContext.Provider></ViewerHostContext.Provider>
  const mounted = render(tree('report'))
  await screen.findByRole('heading', { name: 'Report' })
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(target.querySelector('.embedded-viewer')).toBeTruthy()
  const review = screen.getByRole('button', { name: /Review/ })
  fireEvent.click(review)
  expect(value.keep).toHaveBeenCalledWith('report')
  await waitFor(() => expect(chat.reviewFor).toHaveBeenCalled())
  const body = target.querySelector('.artifact-body')
  mounted.rerender(tree('chat'))
  expect(target.querySelector('.hosted-view')).toHaveAttribute('hidden')
  mounted.rerender(tree('report'))
  expect(target.querySelector('.artifact-body')).toBe(body)
  expect(files.rawCalls).toEqual(['a1'])
  mounted.unmount(); target.remove()
})

it('defers restored inactive viewers until selected and retains the commit session identity', async () => {
  const files = createFakeFiles()
  const read = vi.fn(async () => null)
  const target = document.createElement('div'); document.body.appendChild(target)
  const value = host({ target, views: [{ id: 'commit', view: { kind: 'commit', sha: 'abcdef12', sessionId: 'child-session' } }] })
  const tree = (active: string) => <ViewerHostContext.Provider value={{ ...value, active }}><HostedViewers sessionId="parent-session" files={{ ...files, commitDiff: read }} /></ViewerHostContext.Provider>
  const mounted = render(tree('chat'))
  expect(read).not.toHaveBeenCalled()
  mounted.rerender(tree('commit'))
  await waitFor(() => expect(read).toHaveBeenCalledWith('child-session', 'abcdef12', expect.any(AbortSignal)))
  mounted.unmount(); target.remove()
})

it('refreshes the visible Files list after saving a hosted workspace copy', async () => {
  resetChatState()
  seed(ev({ kind: 'assistant_text', payload: { text: 'Open `notes/a.txt`.' }, seq: 1 }))
  const t = createFakeTransport()
  Object.assign(t.files!, { workspaceFile: async (_session: string, path: string) => ({ path, size: 4, view: 'text', content_type: 'text/plain', blob: new Blob(['kept']) }) })
  const target = document.createElement('div'); document.body.appendChild(target)
  function HostedChat() {
    const [view, setView] = useState<HostedView | null>(null)
    return <ViewerHostContext.Provider value={host({ target, open: setView, active: view ? 'file' : 'chat', views: view ? [{ id: 'file', view }] : [] })}>
      <ChatView session={SESSION} transport={t.transport} meta={meta()} canOpenWorkspaceFiles />
    </ViewerHostContext.Provider>
  }
  const mounted = render(<HostedChat />)
  try {
    await waitFor(() => expect(t.files!.listCalls).toBeGreaterThan(0))
    fireEvent.click(screen.getByRole('button', { name: 'Files (0)' }))
    await waitFor(() => expect(t.files!.listCalls).toBeGreaterThan(1))
    const before = t.files!.listCalls
    fireEvent.click(await screen.findByTitle('Open from the session’s folder'))
    fireEvent.click(await screen.findByRole('button', { name: 'Save a copy to Files' }))
    await screen.findByText('Saved a copy to Files.')
    expect(t.files!.uploads).toHaveLength(1)
    await waitFor(() => expect(t.files!.listCalls).toBeGreaterThan(before))
    expect(await screen.findByTestId('artifact-row')).toHaveTextContent('a.txt')
    expect(screen.getByRole('button', { name: 'Files (1)' })).toBeInTheDocument()
  } finally { mounted.unmount(); target.remove() }
})

it('does not revisit unchanged viewer data on unrelated parent updates', async () => {
  const files = createFakeFiles(); files.serve('# Report')
  const target = document.createElement('div'); document.body.appendChild(target)
  const value = host({ target, active: 'report', views: [{ id: 'report', view: { kind: 'artifact', artifact } }] })
  const artifacts = [artifact]
  const lookup = vi.spyOn(artifacts, 'find')
  const tree = (tick: number) => <ViewerHostContext.Provider value={value}><span>{tick}</span><HostedViewers sessionId="s1" files={files} artifacts={artifacts} /></ViewerHostContext.Provider>
  const mounted = render(tree(0))
  try {
    await screen.findByRole('heading', { name: 'Report' })
    const before = lookup.mock.calls.length
    for (let tick = 1; tick <= 3; tick++) mounted.rerender(tree(tick))
    expect(lookup.mock.calls.length).toBe(before)
  } finally { mounted.unmount(); target.remove(); lookup.mockRestore() }
})

it('uses a single accessible toolbar for hosted workspace files and keeps overlay controls', async () => {
  const files = createFakeFiles()
  Object.assign(files, { workspaceFile: async () => ({ path: 'src/main.go', size: 27000, view: 'text', content_type: 'text/plain', blob: new Blob(['package main']) }) })
  const target = document.createElement('div'); document.body.appendChild(target)
  const value = host({ target, active: 'file', views: [{ id: 'file', preview: true, view: { kind: 'workspace', path: 'src/main.go' } }] })
  const mounted = render(<ViewerHostContext.Provider value={value}><HostedViewers sessionId="s1" files={files} /></ViewerHostContext.Provider>)
  try {
    await screen.findByTestId('workspace-code')
    expect(screen.queryByRole('button', { name: 'Keep open' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Close' })).toBeNull()
    expect(screen.getByRole('toolbar', { name: 'File actions' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Copy src/main.go path' })).toHaveAttribute('title', expect.stringContaining('as of now'))
    expect(target.querySelector('footer')).toBeNull()
    mounted.rerender(<WorkspaceFileViewer sessionId="s1" files={files} path="src/main.go" onClose={() => {}} headerActions={<button>Keep open</button>} />)
    await screen.findByRole('dialog')
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Keep open' })).toBeInTheDocument()
  } finally { mounted.unmount(); target.remove() }
})

it('opens hosted diffs directly on the patch and keeps the review flow behind its toolbar action', async () => {
  const diff = { ...artifact, id: 'patch', name: 'changes.diff', view: 'diff', content_type: 'text/x-diff' }
  const files = createFakeFiles(); files.serve('diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n')
  const target = document.createElement('div'); document.body.appendChild(target)
  const value = host({ target, active: 'patch', views: [{ id: 'patch', view: { kind: 'artifact', artifact: diff } }] })
  const chat: ChatContextValue = { sessionId: 's1', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined, submitFeedback: vi.fn(async () => true), reviewFor: vi.fn(async () => null) }
  const mounted = render(<ViewerHostContext.Provider value={value}><ChatContext.Provider value={chat}><HostedViewers sessionId="s1" files={files} /></ChatContext.Provider></ViewerHostContext.Provider>)
  try {
    await screen.findByTestId('diff-view')
    expect(screen.queryByRole('tab', { name: 'Conversation' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Review' }))
    await screen.findByRole('tab', { name: 'Conversation' })
    expect(chat.reviewFor).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    await screen.findByTestId('diff-view')
    expect(screen.queryByRole('tab', { name: 'Conversation' })).toBeNull()
  } finally { mounted.unmount(); target.remove() }
})

it('renders visible views in independent stable targets and keeps inactive renderer state', async () => {
  const files = createFakeFiles(); files.serve('# Shared report')
  const left = document.createElement('div'), right = document.createElement('div')
  document.body.append(left, right)
  const value = host({ active: 'second', targetFor: id => id === 'first' ? left : right, visible: ['first', 'second'], views: [
    { id: 'first', view: { kind: 'artifact', artifact } },
    { id: 'second', view: { kind: 'artifact', artifact: { ...artifact, id: 'a2', name: 'second.md' } } },
  ] })
  const tree = (visible: string[]) => <ViewerHostContext.Provider value={{ ...value, visible }}><HostedViewers sessionId="s1" files={files}/></ViewerHostContext.Provider>
  const mounted = render(tree(['first', 'second']))
  try {
    await screen.findAllByRole('heading', { name: 'Shared report' })
    expect(left.querySelector('.embedded-viewer')).toBeTruthy(); expect(right.querySelector('.embedded-viewer')).toBeTruthy()
    const leftBody = left.querySelector('.artifact-body'), rightBody = right.querySelector('.artifact-body')
    mounted.rerender(tree(['second']))
    expect(left.querySelector('.hosted-view')).toHaveAttribute('hidden')
    mounted.rerender(tree(['first', 'second']))
    expect(left.querySelector('.artifact-body')).toBe(leftBody); expect(right.querySelector('.artifact-body')).toBe(rightBody)
  } finally { mounted.unmount(); left.remove(); right.remove() }
})

it('places one mounted Chat and its composer in a split host even when another group is focused', () => {
  resetChatState(); seed(ev({ kind: 'assistant_text', payload: { text: 'Ready for review.' }, seq: 1 }))
  const t = createFakeTransport(), target = document.createElement('div'), nextGroup = document.createElement('div')
  document.body.append(target, nextGroup)
  const value = host({ active: 'file', chatTarget: target, visible: ['chat', 'file'] })
  const tree = (visible: string[]) => <ViewerHostContext.Provider value={{ ...value, visible }}><ChatView session={SESSION} transport={t.transport} meta={meta()}/></ViewerHostContext.Provider>
  const mounted = render(tree(['chat', 'file']))
  try {
    const input = target.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
    expect(input).toBeTruthy(); expect(input.closest('.chat-composer-surface')).not.toHaveAttribute('hidden')
    fireEvent.change(input, { target: { value: 'Draft survives moving groups' } })
    nextGroup.append(target)
    mounted.rerender(tree(['file']))
    expect(input.closest('.chat-composer-surface')).toHaveAttribute('hidden')
    mounted.rerender(tree(['chat', 'file']))
    expect(nextGroup.querySelector('.agent-composer textarea')).toBe(input)
    expect(input).toHaveValue('Draft survives moving groups')
  } finally { mounted.unmount(); target.remove(); nextGroup.remove() }
})
