import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render as renderRaw, screen, waitFor } from '@testing-library/react'
import type { DescribeUserMessage } from '@blerglab/chat'
import type { ReactNode } from 'react'
import { MemoryRouter } from 'react-router-dom'
const render = (ui: ReactNode) => renderRaw(ui, { wrapper: MemoryRouter })
import AgentChatView from './AgentChatView'
import { makeSession } from '../test/fixtures'
import * as ws from '../ws'
import { useSessionStore } from '../hooks/useSessionStore'

const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('@blerglab/chat', async () => ({
  ...await vi.importActual<typeof import('@blerglab/chat')>('@blerglab/chat'),
  ChatView: ({ slots, maxFileBytes, canOpenWorkspaceFiles, canAnswerApprovals, describeUserMessage, readOnly }: { slots: { header: ReactNode }; readOnly?: boolean; maxFileBytes?: number; canOpenWorkspaceFiles?: boolean; canAnswerApprovals?: boolean | null; describeUserMessage?: DescribeUserMessage }) => (
    <div data-testid="chat" data-read-only={String(!!readOnly)} data-answer-approvals={String(canAnswerApprovals)} data-session-author={describeUserMessage?.({} as never, { source: 'session', text: 'fixture' }, { first: false })?.author} data-session-href={describeUserMessage?.({} as never, { source: 'session', text: 'fixture' }, { first: false })?.authorHref} data-answer-author={describeUserMessage?.({} as never, { source: 'ask_answer', answered_by: 'session', text: 'fixture' }, { first: false })?.author} data-person-answer-author={String(describeUserMessage?.({} as never, { source: 'ask_answer', text: 'fixture' }, { first: false })?.author)} data-max-file-bytes={maxFileBytes ?? 'none'} data-workspace-files={String(canOpenWorkspaceFiles)}>{slots.header}</div>
  ),
  ReadyCard: () => null,
}))
vi.mock('./UsageLimitResume', () => ({ default: () => <span>Resume when limit resets</span> }))

const MODELS = [
  { id: 'fixture-code', name: 'Fixture Code', section: 'main', efforts: ['low', 'ultra'], default_effort: 'low' },
  { id: 'fixture-fast', name: 'Fixture Fast', section: 'main', efforts: ['low', 'high'], default_effort: 'high' },
]

beforeEach(() => {
  vi.clearAllMocks()
  apiFetch.mockImplementation((url: string) => Promise.resolve({ ok: true, json: async () => ({
    models: url.startsWith('/api/models/codex?runtime=cluster') ? MODELS : [], source: 'live',
  }) }))
})

describe('AgentChatView file limit', () => {
  it('passes the server’s advertised per-file limit to the chat, and nothing when it advertises none', () => {
    useSessionStore.setState({ maxFileBytes: 100 * 1024 * 1024 })
    const view = render(<AgentChatView session={makeSession({ kind: 'agent' })} thinking={false} compact={false} />)
    expect(screen.getByTestId('chat')).toHaveAttribute('data-max-file-bytes', String(100 * 1024 * 1024))
    view.unmount()
    useSessionStore.setState({ maxFileBytes: null })
    render(<AgentChatView session={makeSession({ kind: 'agent' })} thinking={false} compact={false} />)
    expect(screen.getByTestId('chat')).toHaveAttribute('data-max-file-bytes', 'none')
  })
})

describe('AgentChatView workspace files', () => {
  it('lets only the session’s owner open the files the agent names', () => {
    const session = makeSession({ kind: 'agent' })
    const view = render(<AgentChatView session={session} thinking={false} compact={false} canReleasePrompt />)
    expect(screen.getByTestId('chat')).toHaveAttribute('data-workspace-files', 'true')
    view.rerender(<AgentChatView session={session} thinking={false} compact={false} canReleasePrompt={false} />)
    expect(screen.getByTestId('chat')).toHaveAttribute('data-workspace-files', 'false')
    view.rerender(<AgentChatView session={session} thinking={false} compact={false} />)
    expect(screen.getByTestId('chat')).toHaveAttribute('data-workspace-files', 'false')
  })
})

describe('AgentChatView owner-only approvals', () => {
  it('passes "not known yet" through while the live state loads', () => {
    const session = makeSession({ kind: 'agent' })
    const view = render(<AgentChatView session={session} thinking={false} compact={false} canAnswerApprovals={null} />)
    expect(screen.getByTestId('chat')).toHaveAttribute('data-answer-approvals', 'null')
    view.rerender(<AgentChatView session={session} thinking={false} compact={false} canReleasePrompt canAnswerApprovals />)
    expect(screen.getByTestId('chat')).toHaveAttribute('data-answer-approvals', 'true')
    view.rerender(<AgentChatView session={session} thinking={false} compact={false} canReleasePrompt />)
    expect(screen.getByTestId('chat')).toHaveAttribute('data-answer-approvals', 'true')
  })
})

describe('AgentChatView model controls', () => {
  it('uses the cluster catalog to switch Codex models and supported efforts', async () => {
    const session = makeSession({ kind: 'agent', runtime: 'cluster', engine: 'codex', daemon_id: 'pod-session', model: 'fixture-code', effort: 'ultra' })
    const view = render(<AgentChatView session={session} thinking={false} compact={false} />)
    await screen.findByRole('option', { name: 'Fixture Fast' })
    expect(screen.getByRole('option', { name: 'ultra' })).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Effort'), { target: { value: 'ultra' } })
    expect(ws.send).toHaveBeenLastCalledWith({ type: 'set_session_model', session_id: session.id, effort: 'ultra' })
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'fixture-fast' } })
    expect(ws.send).toHaveBeenLastCalledWith({ type: 'set_session_model', session_id: session.id, model: 'fixture-fast', effort: 'high' })
    view.rerender(<AgentChatView session={{ ...session, model: 'fixture-fast', effort: 'high' }} thinking={false} compact={false} />)
    expect(screen.queryByRole('option', { name: 'ultra' })).not.toBeInTheDocument()
    expect(screen.getByLabelText('Effort')).toHaveValue('high')
    expect(apiFetch).toHaveBeenCalledWith('/api/models/codex?runtime=cluster', expect.anything())
  })

  it('keeps automatic resume settings out of the conversation header', () => {
    render(<AgentChatView session={makeSession({ kind: 'agent', engine: 'codex' })} thinking={false} compact={false} />)
    expect(screen.queryByText('Resume when limit resets')).not.toBeInTheDocument()
  })

  it('preserves a supported effort while changing models', async () => {
    render(<AgentChatView session={makeSession({ kind: 'agent', runtime: 'cluster', engine: 'codex', model: 'fixture-code', effort: 'low' })} thinking={false} compact={false} />)
    await screen.findByRole('option', { name: 'Fixture Fast' })
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: 'fixture-fast' } })
    expect(ws.send).toHaveBeenLastCalledWith({ type: 'set_session_model', session_id: 'sess-a', model: 'fixture-fast', effort: 'low' })
  })

  it('shows a catalog error and retries without hiding the current model', async () => {
    apiFetch.mockResolvedValueOnce({ ok: false, status: 502, json: async () => ({ error: 'Reconnect the engine in Settings.' }) })
    render(<AgentChatView session={makeSession({ kind: 'agent', runtime: 'cluster', engine: 'codex', model: 'fixture-code' })} thinking={false} compact={false} />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Reconnect the engine in Settings.')
    expect(screen.getByLabelText('Model')).toHaveValue('fixture-code')
    fireEvent.click(screen.getByRole('button', { name: 'Retry models' }))
    await screen.findByRole('option', { name: 'Fixture Fast' })
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('disambiguates the current missing model against catalog choices and sends exact IDs', async () => {
    const native = 'nvidia-nemotron-3.5-lightning-30b-a3b-nvfp4'
    const current = `custom:alpha/${native}`, next = `custom:beta/${native}`
    apiFetch.mockResolvedValue({ ok: true, json: async () => ({ models: [{ id: next, name: next, section: 'main', efforts: [] }], source: 'live' }) })
    render(<AgentChatView session={makeSession({ kind: 'agent', runtime: 'cluster', engine: 'hermes', model: current })} thinking={false} compact={false} />)
    const nextOption = await screen.findByTitle(next)
    const currentOption = screen.getByRole('option', { selected: true })
    expect(currentOption.textContent).not.toBe(nextOption.textContent)
    expect(currentOption).toHaveAttribute('value', current)
    fireEvent.change(screen.getByLabelText('Model'), { target: { value: next } })
    expect(ws.send).toHaveBeenCalledWith(expect.objectContaining({ type: 'set_session_model', model: next }))
  })

  it('keeps workstation catalogs scoped to their own daemon', async () => {
    render(<AgentChatView session={makeSession({ kind: 'agent', runtime: 'daemon', engine: 'codex', daemon_id: 'workstation' })} thinking={false} compact={false} />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledWith('/api/models/codex?daemon_id=workstation', expect.anything()))
  })
})

it('labels session-sourced messages with the listed parent title', () => {
  useSessionStore.setState({ sessions: [makeSession({ id: 'parent', title: 'Parent task' })] })
  render(<AgentChatView session={makeSession({ parent_session_id: 'parent', started_by: { kind: 'session', name: 'Old name' } })} thinking={false} compact={false} />)
  const chat = screen.getByTestId('chat')
  expect(chat).toHaveAttribute('data-session-author', '↑ Parent · Parent task')
  expect(chat).toHaveAttribute('data-session-href', '/sessions/parent')
  // The parent's answer to the child's question is the parent's; the person's own stays "You".
  expect(chat).toHaveAttribute('data-answer-author', '↑ Parent · Parent task')
  expect(chat).toHaveAttribute('data-person-answer-author', 'undefined')
})


it('opens archived sessions read-only without model or approval controls', () => {
  const manage = vi.fn()
  render(<AgentChatView session={makeSession({kind:'agent',archived_at:'2026-10-20T12:00:00Z'})} thinking={false} compact={false} canReleasePrompt onManageChildApproval={manage}/>)
  expect(screen.getByText('Archived session · read only')).toBeInTheDocument()
  expect(screen.getByTestId('chat')).toHaveAttribute('data-read-only','true')
  expect(screen.queryByLabelText('Model')).toBeNull()
  expect(screen.queryByLabelText('Effort')).toBeNull()
  expect(screen.getByTestId('chat')).toHaveAttribute('data-workspace-files','false')
})
