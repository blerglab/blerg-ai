import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev as mk, meta, resetChatState, seed } from '../test/chat'
import type { SessionMeta } from '../transport/types'
import type { AgentEvent, DelegationRequestPayload } from '../types'

const running: SessionMeta = meta({ status: 'running', started_at: '2026-10-14T00:00:00Z' })

function ev(seq: number, kind: AgentEvent['kind'], payload: unknown, ts = '2026-10-14T00:00:00Z'): AgentEvent {
  return mk({ seq, kind, ts, payload })
}

const REQUEST: DelegationRequestPayload = {
  request_id: 'r1',
  engine: 'codex',
  model: 'gpt-5',
  effort: 'high',
  repo: 'blerg',
  runtime: 'daemon',
  daemon_name: 'laptop',
  title: 'Review the migration',
  prompt: 'Read the diff and list the risks.',
  budget: { engine: 'codex', available: true, windows: [{ id: '5h', used_percent: 62 }, { id: '7d', used_percent: 31 }] },
  offered: ['once', 'session', 'always', 'deny'],
  expires_at: '2099-01-01T00:00:00Z',
}

function mount(opts: { decides?: boolean } = { decides: true }, props: { onOpenSession?: (id: string) => void } = {}) {
  const t = createFakeTransport({ decidesDelegations: opts.decides !== false })
  render(<ChatView session={SESSION} transport={t.transport} meta={running} {...props} />)
  return t
}

const seedRequest = (over: Partial<DelegationRequestPayload> = {}) =>
  seed(ev(1, 'user_message', { text: 'go', source: 'chat' }), ev(2, 'delegation_request', { ...REQUEST, ...over }))

describe('the delegation card', () => {
  beforeEach(() => resetChatState())

  it('shows engine, model, the unsandboxed warning, the title, the excerpt and the budget', () => {
    seedRequest()
    mount()
    const card = screen.getByTestId('delegation-card')
    expect(card.textContent).toContain('This session wants to start a Codex session')
    expect(card.textContent).toContain('gpt-5')
    expect(card.textContent).toContain('high')
    expect(card.textContent).toContain('blerg')
    const warn = within(card).getByText('on your desktop, unsandboxed')
    expect(warn.className).toContain('delegation-warn')
    expect(card.textContent).toContain('Review the migration')
    expect(card.textContent).toContain('Read the diff and list the risks.')
    expect(card.textContent).toContain('5h 62% used')
    expect(card.textContent).toContain('7d 31%')
  })

  it('does not warn about the sandbox, and says no repository', () => {
    seedRequest({ runtime: 'docker', repo: undefined, no_repo: true })
    mount()
    const card = screen.getByTestId('delegation-card')
    expect(within(card).getByText('in the desktop sandbox').className).not.toContain('delegation-warn')
    expect(card.textContent).toContain('no repository')
  })

  it('offers only the choices the server offered', () => {
    seedRequest({ offered: ['once', 'deny'] })
    mount()
    const card = screen.getByTestId('delegation-card')
    expect(within(card).getAllByRole('button').map(b => b.textContent)).toEqual(['Allow once', 'Deny'])
  })

  it('names the engine and runtime on the Always button', () => {
    seedRequest()
    mount()
    const always = screen.getByRole('button', { name: 'Always allow Codex on this desktop (unsandboxed)' })
    expect(always.getAttribute('title')).toContain('laptop')
    expect(screen.getByRole('button', { name: 'Allow for this session' })).toBeInTheDocument()
  })

  it('names the sandbox on the Always button for a docker request', () => {
    seedRequest({ runtime: 'docker' })
    mount()
    expect(screen.getByRole('button', { name: 'Always allow Codex in the desktop sandbox' })).toBeInTheDocument()
  })

  it('decides through the transport and shows "Allowed once · <time>" with an Open link', async () => {
    seedRequest()
    const open = vi.fn()
    const t = mount({ decides: true }, { onOpenSession: open })
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Allow once' })) })
    expect(t.decisions).toEqual([{ requestId: 'r1', choice: 'once' }])
    // The server's event closes the card, whatever the click returned.
    act(() => t.emit(ev(3, 'delegation_decided', { request_id: 'r1', status: 'allowed', choice: 'once', child_session_id: 'child-1234567890' }, '2026-10-14T15:12:00Z')))
    const card = screen.getByTestId('delegation-card')
    const time = new Date('2026-10-14T15:12:00Z').toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })
    expect(card.textContent).toContain(`Allowed once · ${time}`)
    expect(within(card).queryByRole('button', { name: 'Allow once' })).toBeNull()
    fireEvent.click(within(card).getByRole('button', { name: 'Open the session' }))
    expect(open).toHaveBeenCalledWith('child-1234567890')
  })

  it('names the child by its short id when the host cannot open it', () => {
    seed(
      ev(1, 'delegation_request', REQUEST),
      ev(2, 'delegation_decided', { request_id: 'r1', status: 'allowed', choice: 'session', child_session_id: 'child-1234567890' }),
    )
    mount()
    const card = screen.getByTestId('delegation-card')
    expect(card.textContent).toContain('Allowed for this session')
    expect(card.textContent).toContain('child-12')
    expect(within(card).queryByRole('button')).toBeNull()
  })

  it('shows Denied and a start error', () => {
    seed(ev(1, 'delegation_request', REQUEST), ev(2, 'delegation_decided', { request_id: 'r1', status: 'allowed', choice: 'once', start_error: 'the daemon is gone' }))
    mount()
    expect(screen.getByTestId('delegation-card').textContent).toContain('the daemon is gone')
  })

  it('disables the buttons while deciding and shows an error inline', async () => {
    seedRequest()
    const t = mount()
    let reject: (e: Error) => void = () => {}
    t.delegation.decideImpl = () => new Promise((_, rej) => { reject = rej })
    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))
    expect(screen.getByRole('button', { name: 'Allow once' })).toBeDisabled()
    await act(async () => { reject(new Error('already decided')) })
    expect(screen.getByTestId('delegation-error').textContent).toBe('already decided')
    expect(screen.getByRole('button', { name: 'Allow once' })).not.toBeDisabled()
  })

  it('two clicks in the same tick post once', async () => {
    seedRequest()
    const t = mount()
    const once = screen.getByRole('button', { name: 'Allow once' })
    await act(async () => {
      fireEvent.click(once)
      fireEvent.click(once)
      fireEvent.click(screen.getByRole('button', { name: 'Deny' }))
    })
    expect(t.decisions).toEqual([{ requestId: 'r1', choice: 'once' }])
  })

  it('after a 409 keeps the buttons off and says Already decided, until the event arrives', async () => {
    seedRequest()
    const t = mount()
    t.delegation.decideImpl = async () => { throw Object.assign(new Error('request already decided'), { status: 409 }) }
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Allow once' })) })
    expect(screen.getByTestId('delegation-error').textContent).toBe('Already decided')
    expect(screen.getByRole('button', { name: 'Allow once' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Deny' })).toBeDisabled()
    act(() => t.emit(ev(3, 'delegation_decided', { request_id: 'r1', status: 'denied' })))
    expect(screen.getByTestId('delegation-card').textContent).toContain('Denied')
  })

  it('a delegation_decided event wins over the browser-clock expiry', () => {
    seed(
      ev(1, 'delegation_request', { ...REQUEST, expires_at: '2026-10-14T00:10:00Z' }),
      ev(2, 'delegation_decided', { request_id: 'r1', status: 'allowed', choice: 'once' }),
    )
    mount()
    const text = screen.getByTestId('delegation-card').textContent
    expect(text).toContain('Allowed once')
    expect(text).not.toContain('Expired')
  })

  it('Show all fetches the whole prompt', async () => {
    seedRequest({ prompt_truncated: true })
    const t = mount()
    t.delegation.prompt = 'Read the diff and list the risks. And then a great deal more.'
    await act(async () => { fireEvent.click(screen.getByRole('button', { name: 'Show all' })) })
    expect(t.fullPrompts).toEqual(['r1'])
    expect(screen.getByTestId('delegation-card').textContent).toContain('a great deal more')
  })

  it('has no Show all when the prompt was not cut or the transport cannot fetch it', () => {
    seedRequest()
    mount()
    expect(screen.queryByRole('button', { name: 'Show all' })).toBeNull()
  })

  it('is read-only without decideDelegation, with "Decide in Blerg Runner"', () => {
    seedRequest()
    mount({ decides: false })
    const card = screen.getByTestId('delegation-card')
    expect(within(card).queryByRole('button')).toBeNull()
    expect(card.textContent).toContain('Decide in Blerg Runner')
  })

  it('a message typed in the composer does not decide', async () => {
    seedRequest()
    const t = mount()
    const box = screen.getByRole('textbox')
    fireEvent.change(box, { target: { value: 'yes, allow it' } })
    await act(async () => { fireEvent.keyDown(box, { key: 'Enter' }) })
    expect(t.sent).toEqual(['yes, allow it'])
    expect(t.decisions).toEqual([])
    expect(screen.getByRole('button', { name: 'Allow once' })).toBeInTheDocument()
  })

  it('says "Expired, nobody answered" for an expired request', () => {
    seedRequest({ expires_at: '2026-10-14T00:10:00Z' })
    mount()
    const card = screen.getByTestId('delegation-card')
    expect(card.textContent).toContain('Expired, nobody answered')
    expect(within(card).queryByRole('button')).toBeNull()
  })

  it('draws nothing for the decided event itself', () => {
    seed(ev(1, 'delegation_request', REQUEST), ev(2, 'delegation_decided', { request_id: 'r1', status: 'denied' }))
    mount()
    expect(screen.getAllByTestId('delegation-card')).toHaveLength(1)
    expect(screen.getByTestId('delegation-card').textContent).toContain('Denied')
  })
})
