import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import BranchSettingsSection from './BranchSettingsSection'
import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: vi.fn(() => 'first') }))
beforeEach(() => { vi.mocked(apiFetch).mockReset(); vi.mocked(signedInAccount).mockReturnValue('first') })
it('loads the account template and saves a new value', async () => {
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({ branch_template: 'dev/{username}/{slug}' })))
  render(<BranchSettingsSection />)
  fireEvent.click(screen.getByText('Work branches'))
  await waitFor(() => expect(screen.getByLabelText('Branch template')).toHaveValue('dev/{username}/{slug}'))
  fireEvent.change(screen.getByLabelText('Branch template'), { target: { value: 'dev/{card}/{slug}' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save branch template' }))
  await waitFor(() => expect(apiFetch).toHaveBeenLastCalledWith('/api/me/preferences', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ branch_template: 'dev/{card}/{slug}' }) })))
  expect(await screen.findByRole('status')).toHaveTextContent('Saved')
})
it('shows a refused template without discarding the draft', async () => {
  vi.mocked(apiFetch).mockResolvedValueOnce(new Response('{}')).mockResolvedValueOnce(new Response(JSON.stringify({ error:'Unknown placeholder.' }), { status:400 }))
  render(<BranchSettingsSection />)
  fireEvent.click(screen.getByText('Work branches'))
  await waitFor(() => expect(screen.getByLabelText('Branch template')).toBeEnabled())
  fireEvent.change(screen.getByLabelText('Branch template'), { target: { value:'dev/{typo}' } })
  fireEvent.click(screen.getByRole('button', { name: 'Save branch template' }))
  expect(await screen.findByRole('alert')).toHaveTextContent('Unknown placeholder.')
  expect(screen.getByLabelText('Branch template')).toHaveValue('dev/{typo}')
})

it('reloads for another account and ignores the previous account response', async () => {
  let finish!: (response: Response) => void
  vi.mocked(apiFetch).mockImplementationOnce(() => new Promise<Response>(resolve => { finish = resolve }))
    .mockResolvedValue(new Response(JSON.stringify({ branch_template: 'dev/second/{slug}' })))
  render(<BranchSettingsSection />)
  fireEvent.click(screen.getByText('Work branches'))
  await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
  act(() => { vi.mocked(signedInAccount).mockReturnValue('second'); window.dispatchEvent(new Event('blerg-identity-changed')) })
  await waitFor(() => expect(screen.getByLabelText('Branch template')).toHaveValue('dev/second/{slug}'))
  await act(async () => { finish(new Response(JSON.stringify({ branch_template: 'dev/first/{slug}' }))) })
  expect(screen.getByLabelText('Branch template')).toHaveValue('dev/second/{slug}')
})

it('leaves the default template empty and explains readable branch names', async () => {
  vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({})))
  render(<BranchSettingsSection />)
  fireEvent.click(screen.getByText('Work branches'))
  await waitFor(() => expect(screen.getByLabelText('Branch template')).toBeEnabled())
  expect(screen.getByLabelText('Branch template')).toHaveValue('')
  expect(screen.getByText(/Leave blank for/)).toBeInTheDocument()
})
