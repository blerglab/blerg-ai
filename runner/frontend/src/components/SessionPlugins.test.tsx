import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { useState } from 'react'
import { PluginAddButton, PluginAddForm, PluginRequests, SessionPluginChatRequests } from './SessionPlugins'
import { useSessionPlugins } from '../hooks/useSessionPlugins'
import type { SessionPluginSettings } from '../lib/sessionPlugins'
const apiFetch = vi.fn()
vi.mock('../apiFetch', () => ({ apiFetch: (...args: unknown[]) => apiFetch(...args) }))
const settings: SessionPluginSettings = {
  engine: 'claude',
  supported: true,
  session_apply: true,
  allowed_marketplaces: ['example/plugins'],
  any_marketplace: false,
  plugins: [],
  requests: [],
}
const reply = (body: unknown, status = 200) =>
  Promise.resolve({ ok: status < 400, status, json: async () => body })
function Harness({ session = 's1' }: { session?: string }) {
  const model = useSessionPlugins(session, true)
  const [open, setOpen] = useState(false)
  return (
    <MemoryRouter>
      <PluginAddButton open={open} onToggle={() => setOpen(o => !o)} controls="add-form" />
      {open && <PluginAddForm model={model} id="add-form" />}
      <PluginRequests model={model} />
    </MemoryRouter>
  )
}
beforeEach(() => {
  apiFetch.mockReset()
  apiFetch.mockImplementation(() => reply(settings))
})
describe('session plugin controls', () => {
  it('uses server marketplace policy and only writes on an explicit human add', async () => {
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add plugin or skill' }))
    expect(screen.getByLabelText('Marketplace')).toHaveTextContent('example/plugins')
    fireEvent.change(screen.getByLabelText('Plugin name'), { target: { value: 'review-helper' } })
    expect(apiFetch.mock.calls.filter((c) => c[1]?.method === 'POST')).toHaveLength(0)
    apiFetch.mockImplementation((_url: string, init?: RequestInit) =>
      init?.method === 'POST' ? reply({ id: 'p1', state: 'executing' }, 202) : reply(settings),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Add to session' }))
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        '/api/sessions/s1/plugins',
        expect.objectContaining({
          body: JSON.stringify({ marketplace: 'example/plugins', plugin: 'review-helper', scope: 'session' }),
        }),
      ),
    )
  })
  it('makes unsupported live application explicitly always-on for new sessions', async () => {
    apiFetch.mockImplementation(() =>
      reply({
        ...settings,
        engine: 'codex',
        session_apply: false,
        reason: 'Changes apply to new Codex sessions.',
      }),
    )
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add plugin or skill' }))
    expect(screen.getByRole('option', { name: 'This session only' })).toBeDisabled()
    expect(screen.getByText('Changes apply to new Codex sessions.')).toBeInTheDocument()
    expect(screen.getByLabelText('Apply plugin')).toHaveValue('always')
    expect(screen.getByRole('button', { name: 'Save always on' })).toBeDisabled()
  })
  it('fails closed on discovery failure and allows retry', async () => {
    apiFetch.mockImplementationOnce(() => reply({}, 503))
    render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Add plugin or skill' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load plugin choices')
    expect(screen.queryByLabelText('Plugin name')).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Retry plugin choices' }))
    expect(await screen.findByLabelText('Plugin name')).toBeInTheDocument()
  })
  it('shows request approval links and application state without treating agent text as markup', async () => {
    apiFetch.mockImplementation(() =>
      reply({
        ...settings,
        requests: [
          {
            id: 'p1',
            kind: 'plugin_request',
            state: 'pending',
            arguments: { plugin: 'helper', marketplace: 'example/plugins', reason: '<img src=x>' },
            result: null,
          },
          {
            id: 'p2',
            kind: 'plugin_request',
            state: 'done',
            arguments: { plugin: 'later', marketplace: 'example/plugins' },
            result: { state: 'new_sessions', message: 'Available for new sessions.' },
          },
        ],
      }),
    )
    render(<Harness />)
    expect(await screen.findByText('<img src=x>')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Approve or decline helper' })).toHaveAttribute(
      'href',
      '/proposals?id=p1',
    )
    expect(screen.getByText('Available for new sessions.')).toBeInTheDocument()
    expect(document.querySelector('img')).toBeNull()
  })
  it('clears previous session choices while the next session is loading', async () => {
    const view = render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add plugin or skill' }))
    await screen.findByLabelText('Plugin name')
    apiFetch.mockImplementation(() => new Promise(() => {}))
    view.rerender(<Harness session="s2" />)
    expect(screen.queryByLabelText('Plugin name')).toBeNull()
  })
})

describe('plugin scope and chat privacy', () => {
  it('posts always-on scope explicitly for a runtime that cannot load live plugins', async () => {
    const next = { ...settings, session_apply: false }
    apiFetch.mockImplementation((_url: string, init?: RequestInit) =>
      init?.method === 'POST'
        ? reply({ id: 'p1', state: 'done', result: { state: 'new_sessions' } }, 202)
        : reply(next),
    )
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add plugin or skill' }))
    fireEvent.change(await screen.findByLabelText('Plugin name'), { target: { value: 'helper' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save always on' }))
    await waitFor(() =>
      expect(apiFetch).toHaveBeenCalledWith(
        '/api/sessions/s1/plugins',
        expect.objectContaining({
          body: JSON.stringify({ marketplace: 'example/plugins', plugin: 'helper', scope: 'always' }),
        }),
      ),
    )
    expect(await screen.findByRole('status')).toHaveTextContent('Saved for new sessions')
  })
  it('keeps a failed human add editable and never reports it as applied', async () => {
    apiFetch.mockImplementation((_url: string, init?: RequestInit) =>
      init?.method === 'POST' ? reply({}, 409) : reply(settings),
    )
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add plugin or skill' }))
    fireEvent.change(await screen.findByLabelText('Plugin name'), { target: { value: 'helper' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add to session' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('not accepted')
    expect(screen.getByLabelText('Plugin name')).toHaveValue('helper')
    expect(screen.queryByText('Loaded in this session.')).toBeNull()
  })
})

describe('owner-only plugin request cards beside chat', () => {
  it('does not fetch private requests for a reader who cannot manage the session', () => {
    render(
      <MemoryRouter>
        <SessionPluginChatRequests sessionId="private" active={false} />
      </MemoryRouter>,
    )
    expect(apiFetch).not.toHaveBeenCalled()
  })
  it('shows only pending requests to the owner, with a native approval link', async () => {
    apiFetch.mockImplementation(() =>
      reply({
        ...settings,
        requests: [
          {
            id: 'p1',
            state: 'pending',
            arguments: { plugin: 'helper', reason: 'Owner-only reason' },
            result: null,
          },
          { id: 'p2', state: 'rejected', arguments: { plugin: 'declined' }, result: null },
        ],
      }),
    )
    render(
      <MemoryRouter>
        <SessionPluginChatRequests sessionId="s1" active />
      </MemoryRouter>,
    )
    expect(await screen.findByText('Owner-only reason')).toBeInTheDocument()
    expect(screen.queryByText('declined')).toBeNull()
    expect(screen.getByRole('link', { name: 'Approve or decline helper' })).toHaveAttribute(
      'href',
      '/proposals?id=p1',
    )
  })
})

describe('wildcard marketplace policy', () => {
  it('accepts an empty Go slice encoded as null and still requires an explicit valid marketplace', async () => {
    apiFetch.mockImplementation(() =>
      reply({ ...settings, allowed_marketplaces: null, any_marketplace: true }),
    )
    render(<Harness />)
    fireEvent.click(await screen.findByRole('button', { name: 'Add plugin or skill' }))
    const source = await screen.findByLabelText('Marketplace')
    fireEvent.change(screen.getByLabelText('Plugin name'), { target: { value: 'helper' } })
    expect(screen.getByRole('button', { name: 'Add to session' })).toBeDisabled()
    fireEvent.change(source, { target: { value: 'example/plugins' } })
    expect(screen.getByRole('button', { name: 'Add to session' })).toBeEnabled()
  })
})
