import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import NewChannel from './NewChannel'
import { apiFetch } from '../apiFetch'
import { makeSession } from '../test/fixtures'

vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../lib/launchOptions', () => ({ useLaunchOptions: () => ({ state: 'known', engines: [{ id: 'codex', name: 'Codex' }], retry: vi.fn() }) }))
afterEach(() => vi.clearAllMocks())

it('retains the name after a refused launch and permits retry', async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('{"error":"Workspace runtime unavailable"}', { status: 409 }))
  const close = vi.fn()
  render(<MemoryRouter><NewChannel workspace={makeSession({ id: 'main' })} close={close} /></MemoryRouter>)
  fireEvent.change(screen.getByRole('textbox', { name: 'Channel name' }), { target: { value: 'Design' } })
  fireEvent.click(screen.getByRole('button', { name: 'Start channel' }))
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Workspace runtime unavailable'))
  expect(screen.getByRole('textbox', { name: 'Channel name' })).toHaveValue('Design')
  expect(screen.getByRole('button', { name: 'Start channel' })).toBeEnabled()
  expect(close).not.toHaveBeenCalled()
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('{"session_id":"channel"}', { status: 202 }))
  fireEvent.click(screen.getByRole('button', { name: 'Start channel' }))
  await waitFor(() => expect(close).toHaveBeenCalledOnce())
})

it('closes with Escape without creating a channel', () => {
  const close = vi.fn()
  render(<MemoryRouter><NewChannel workspace={makeSession({ id: 'main' })} close={close} /></MemoryRouter>)
  fireEvent.keyDown(screen.getByRole('textbox', { name: 'Channel name' }), { key: 'Escape' })
  expect(close).toHaveBeenCalledOnce()
  expect(apiFetch).not.toHaveBeenCalled()
})

it('shows a plain error for a non-JSON failure and reuses the idempotency key on retry', async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('Bad Gateway', { status: 502 }))
  render(<MemoryRouter><NewChannel workspace={makeSession({ id: 'main' })} close={vi.fn()} /></MemoryRouter>)
  fireEvent.change(screen.getByRole('textbox', { name: 'Channel name' }), { target: { value: 'Design' } })
  fireEvent.click(screen.getByRole('button', { name: 'Start channel' }))
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('Could not start the channel.'))
  const first = new Headers(vi.mocked(apiFetch).mock.calls[0][1]?.headers).get('Idempotency-Key')
  expect(first).toBeTruthy()
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('{"session_id":"channel"}', { status: 202 }))
  fireEvent.click(screen.getByRole('button', { name: 'Start channel' }))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
  expect(new Headers(vi.mocked(apiFetch).mock.calls[1][1]?.headers).get('Idempotency-Key')).toBe(first)
})
