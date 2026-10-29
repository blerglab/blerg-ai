import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { createRef, useLayoutEffect, type ReactNode } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, expect, it, vi } from 'vitest'
import CommandPaletteProvider, { PaletteButton } from './CommandPalette'
import { ChildDiscardAction, ParentDiscardAction } from './ChildDiscard'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import { usePalette, usePaletteCommands } from '../commands/context'
import { type CommandRegistry } from '../commands/registry'
import { HostShellConsentRequired } from '../shellTransport'

import { apiFetch } from '../apiFetch'
import { FakeShell } from '../test/shellMock'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('./ShellView', () => ({ default: () => <div>Terminal</div> }))
let registry: CommandRegistry
function Controls({ title }: { title: string }) {
  const palette = usePalette()
  useLayoutEffect(() => { registry = palette!.registry }, [palette])
  usePaletteCommands([{ id: 'safe-help', title: `${title} help`, destructive: false, run: vi.fn() }])
  return <PaletteButton />
}
function show(title: string, feature: ReactNode) {
  render(<MemoryRouter><CommandPaletteProvider><Controls title={title} />{feature}</CommandPaletteProvider></MemoryRouter>)
}
function search(query: string) {
  fireEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
  const input = screen.getByRole('combobox', { name: /^Search/ })
  fireEvent.change(input, { target: { value: `>${query}` } })
  return input
}
function onlyRestricted(query: string) {
  const input = screen.getByRole('combobox', { name: /^Search/ })
  fireEvent.change(input, { target: { value: query } })
  expect(screen.getAllByRole('option')).toHaveLength(1)
  expect(screen.getByRole('option')).toHaveClass('palette-destructive')
  expect(screen.queryByRole('option', { selected: true })).toBeNull()
  expect(input).not.toHaveAttribute('aria-activedescendant')
  fireEvent.keyDown(input, { key: 'Enter' })
  return input
}
beforeEach(() => { localStorage.clear(); vi.mocked(apiFetch).mockReset() })

it.each(['parent', 'child'] as const)('protects the real %s Discard registration, including recency and confirmation', async kind => {
  const focusTarget = createRef<HTMLButtonElement>()
  show('Discard', <><button ref={focusTarget}>Session actions</button>{kind === 'parent'
    ? <ParentDiscardAction focusTarget={focusTarget} sessionId="session" title="Work" palette />
    : <ChildDiscardAction focusTarget={focusTarget} parentId="parent" childId="session" title="Work" palette />}</>)
  const id = 'discard-session:session'
  expect(registry.getSnapshot().items.find(item => item.id === id)?.destructive).toBe(true)
  registry.remember(id)
  search('Discard')
  expect(screen.getAllByRole('option').map(option => option.textContent?.trim())).toEqual(['Discard help', 'Discard session…'])
  expect(screen.getByRole('option', { selected: true })).toHaveTextContent('Discard help')
  const remember = vi.spyOn(registry, 'remember')
  const input = onlyRestricted('Discard session')
  expect(screen.queryByRole('alertdialog')).toBeNull()
  expect(apiFetch).not.toHaveBeenCalled()
  fireEvent.keyDown(input, { key: 'ArrowDown' })
  fireEvent.keyDown(input, { key: 'Enter' })
  let confirm = screen.getByRole('alertdialog')
  expect(confirm).toHaveAccessibleName(kind === 'parent' ? 'Discard session and all children?' : 'Discard child session?')
  expect(apiFetch).not.toHaveBeenCalled()
  fireEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
  expect(remember).not.toHaveBeenCalled()
  search('Discard')
  expect(screen.getByRole('option', { selected: true })).toHaveTextContent('Discard help')
  onlyRestricted('Discard session')
  fireEvent.click(screen.getByRole('option', { name: 'Discard session…' }))
  confirm = screen.getByRole('alertdialog')
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ status: 'discarded', session_id: 'session', archived_at: '2026-10-23T00:00:00Z' })))
  fireEvent.click(within(confirm).getByRole('button', { name: kind === 'parent' ? 'Discard session and children' : 'Discard child session' }))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledWith(kind === 'parent' ? '/api/sessions/session/discard' : '/api/sessions/parent/children/session/discard', { method: 'POST' }))
  expect(remember).not.toHaveBeenCalled()
})

it('protects real New shell consent before launch and after cancellation', async () => {
  const challenge = { challenge_id: 'challenge', machine_id: 'machine', account: 'user', uid: 1000, shell: '/bin/bash', cwd: '/project' }
  const createShell = vi.fn(async () => { throw new HostShellConsentRequired(challenge) })
  show('New', <ShellWorkspace sessionId="session" accountId="owner" createShell={createShell} restoreShells={async () => []}><textarea aria-label="Composer" /></ShellWorkspace>)
  await act(async () => {})
  expect(registry.getSnapshot().items.find(item => item.id === 'toggle-shell')?.destructive).toBe(true)
  registry.remember('toggle-shell')
  search('New')
  expect(screen.getAllByRole('option').map(option => option.textContent?.trim())).toEqual(['New help', 'New shell'])
  const remember = vi.spyOn(registry, 'remember')
  const input = onlyRestricted('New shell')
  expect(createShell).not.toHaveBeenCalled()
  fireEvent.keyDown(input, { key: 'ArrowDown' })
  fireEvent.keyDown(input, { key: 'Enter' })
  const consent = await screen.findByRole('dialog', { name: 'Allow a shell on this machine?' })
  expect(createShell).toHaveBeenCalledOnce()
  expect(apiFetch).not.toHaveBeenCalled()
  fireEvent.click(within(consent).getByRole('button', { name: 'Cancel' }))
  expect(remember).not.toHaveBeenCalled()
  search('New')
  expect(screen.getByRole('option', { selected: true })).toHaveTextContent('New help')
  onlyRestricted('New shell')
  expect(createShell).toHaveBeenCalledOnce()
  expect(apiFetch).not.toHaveBeenCalled()
})

it('retains shell safety when focusing an existing shell', async () => {
  const restored = new FakeShell('restored'), createShell = vi.fn()
  show('Focus', <ShellWorkspace sessionId="session" accountId="owner" createShell={createShell} restoreShells={async () => [restored]}><div>Chat</div></ShellWorkspace>)
  await screen.findByLabelText('Close Shell')
  search('Focus shell')
  expect(screen.getByRole('option', { name: /^Focus shell/ })).toHaveClass('palette-destructive')
  expect(screen.getByRole('combobox', { name: /^Search/ })).not.toHaveAttribute('aria-activedescendant')
  fireEvent.click(screen.getByRole('option', { name: /^Focus shell/ }))
  expect(screen.getByRole('tab', { name: 'Shell' })).toHaveAttribute('aria-selected', 'true')
  expect(createShell).not.toHaveBeenCalled()
  expect(restored.ended).toBe(false)
})
