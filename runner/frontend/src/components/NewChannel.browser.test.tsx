import { afterEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { userEvent, page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import SessionDockWindows from './SessionDockWindows'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeSession } from '../test/fixtures'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => null, ensureFreshToken: async () => false }))
afterEach(() => vi.restoreAllMocks())
for (const width of [390, 3840]) it(`starts a named channel with defaults or changed options using the keyboard at ${width}px`, async () => {
  localStorage.clear()
  await page.viewport(width, 900)
  useSessionStore.setState({ sessions: [makeSession({ id: 'main', runtime: 'docker', engine: 'codex', workspace: { members: [{ id: 'repo-a', name: 'App', path: 'app' }, { id: 'repo-b', name: 'Docs', path: 'docs' }] } })] })
  const requests: { url: string; body: unknown }[] = []
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input)
    if (url.includes('launch-options')) return new Response(JSON.stringify({ state: 'known', engines: [{ id: 'codex', name: 'Codex' }, { id: 'hermes', name: 'Hermes' }] }))
    if (url.endsWith('/channels')) { requests.push({ url, body: JSON.parse(String(init?.body)) }); return new Response('{"session_id":"new-channel"}', { status: 202 }) }
    if (url.endsWith('/categories')) return new Response('{"categories":[]}')
    return new Response('{}')
  })
  render(<MemoryRouter><div style={{ display: 'flex', width: width === 390 ? '100%' : 320, height: 750 }}><SessionDockWindows sessionId="main" mobile={width === 390} files={<p>Files</p>} /></div></MemoryRouter>)
  const button = screen.getByRole('button', { name: 'New channel' })
  button.focus()
  await userEvent.keyboard('{Enter}')
  const name = screen.getByRole('textbox', { name: 'Channel name' })
  expect(name).toHaveFocus()
  await userEvent.type(name, 'Design')
  await waitFor(() => expect(screen.getByRole('combobox', { name: 'Engine' })).toHaveValue('codex'))
  fireEvent.click(screen.getByText('Change repositories and permissions'))
  expect(screen.getByRole('checkbox', { name: 'Inherit workspace permissions' })).toBeChecked()
  expect(screen.getByRole('checkbox', { name: 'App' })).toBeChecked()
  expect(screen.getByRole('checkbox', { name: 'Docs' })).toBeChecked()
  if (width === 3840) {
    fireEvent.change(screen.getByRole('combobox', { name: 'Engine' }), { target: { value: 'hermes' } })
    await userEvent.click(screen.getByRole('checkbox', { name: 'Docs' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'Inherit workspace permissions' }))
    await userEvent.click(screen.getByRole('checkbox', { name: 'Private chat' }))
  }
  if (width === 390) {
    for (const name of ['Start channel', 'Cancel']) expect(screen.getByRole('button', { name }).getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
    for (const checkbox of screen.getAllByRole('checkbox')) {
      expect(checkbox).toHaveAccessibleName()
      expect(checkbox.closest('label')!.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
    }
  }
  const form = screen.getByRole('form', { name: 'New channel' })
  expect(screen.getByRole('combobox', { name: 'Engine' }).getBoundingClientRect().width).toBeLessThan(form.getBoundingClientRect().width - 80)
  expect(form.scrollWidth).toBeLessThanOrEqual(form.clientWidth)
  await userEvent.click(screen.getByRole('button', { name: 'Start channel' }))
  await waitFor(() => expect(requests).toHaveLength(1))
  expect(requests[0]).toEqual({ url: '/api/sessions/main/channels', body: { title: 'Design', engine: width === 390 ? 'codex' : 'hermes', repos: width === 390 ? ['repo-a', 'repo-b'] : ['repo-a'], inherit_permissions: width === 390, private: width !== 390 } })
})
