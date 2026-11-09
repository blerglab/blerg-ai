import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import SkillSettingsSection from './SkillSettingsSection'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: vi.fn(() => 'first') }))
const defaults = { install: {}, account: {}, workspace: {}, effective: {}, admin: true, workspaces: [{ id: 'workspace-one', title: 'Build the garden' }] }
beforeEach(() => { vi.mocked(apiFetch).mockReset(); vi.mocked(signedInAccount).mockReturnValue('first') })
async function open(data = defaults) {
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify(data)))
  render(<SkillSettingsSection />)
  fireEvent.click(screen.getByText('Skills'))
  await waitFor(() => expect(screen.getByLabelText('Test-driven development')).toBeEnabled())
}
it('defaults on and writes only the changed account switch', async () => {
  await open()
  expect(screen.getByLabelText('UX design')).toBeChecked()
  fireEvent.click(screen.getByLabelText('Test-driven development'))
  expect(await screen.findByRole('status')).toHaveTextContent('Saved')
  expect(apiFetch).toHaveBeenLastCalledWith('/api/skills/settings', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ scope: 'account', skills: { 'test-driven-development': false } }) }))
  expect(screen.getByText('Coordinating · always on for Main')).toBeInTheDocument()
  expect(screen.getAllByRole('checkbox')).toHaveLength(3)
})
it('shows administrator precedence and offers workspace and install scopes', async () => {
  await open({ ...defaults, install: { 'ux-design': false } })
  expect(screen.getByLabelText('UX design')).toBeDisabled()
  expect(screen.getByLabelText('UX design')).not.toBeChecked()
  expect(screen.getByText('Disabled by administrator')).toBeInTheDocument()
  fireEvent.change(screen.getByLabelText('Apply to'), { target: { value: 'workspace-one' } })
  await waitFor(() => expect(apiFetch).toHaveBeenLastCalledWith('/api/skills/settings?workspace=workspace-one', expect.anything()))
  await waitFor(() => expect(screen.getByLabelText('Test-driven development')).toBeEnabled())
  fireEvent.click(screen.getByLabelText('Test-driven development'))
  await waitFor(() => expect(apiFetch).toHaveBeenLastCalledWith('/api/skills/settings?workspace=workspace-one', expect.objectContaining({ body: JSON.stringify({ scope: 'workspace', skills: { 'test-driven-development': false } }) })))
})
it('reports failed saves and reloads on an account switch', async () => {
  await open()
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('', { status: 503 }))
  fireEvent.click(screen.getByLabelText('UX design'))
  expect(await screen.findByRole('alert')).toHaveTextContent('Could not save')
  vi.mocked(apiFetch).mockImplementation(async () => new Response(JSON.stringify({ ...defaults, admin: false })))
  act(() => { vi.mocked(signedInAccount).mockReturnValue('second'); window.dispatchEvent(new Event('blerg-identity-changed')) })
  await waitFor(() => expect(screen.getByLabelText('UX design')).toBeEnabled())
  expect(screen.queryByRole('option', { name: 'This install' })).toBeNull()
})

it('reloads the install scope and lets an administrator re-enable its disabled skill', async () => {
  await open({ ...defaults, install: { 'ux-design': false } })
  expect(screen.getByLabelText('UX design')).toBeDisabled()
  fireEvent.change(screen.getByLabelText('Apply to'), { target: { value: 'install' } })
  await waitFor(() => expect(screen.getByLabelText('UX design')).toBeEnabled())
  expect(screen.getByLabelText('UX design')).not.toBeChecked()
  fireEvent.click(screen.getByLabelText('UX design'))
  await waitFor(() => expect(apiFetch).toHaveBeenLastCalledWith('/api/skills/settings', expect.objectContaining({ body: JSON.stringify({ scope: 'install', skills: { 'ux-design': true } }) })))
})
