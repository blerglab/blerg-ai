import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, expect, it, vi } from 'vitest'
import { apiFetch } from '../apiFetch'
import { VerbositySettings, SessionVerbosity } from './VerbositySelect'
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
beforeEach(() => vi.mocked(apiFetch).mockReset())
it('loads and saves the account default', async () => {
 vi.mocked(apiFetch).mockResolvedValue(new Response('{"verbosity":"terse"}'))
 render(<VerbositySettings />)
 fireEvent.click(screen.getByText('Session verbosity'))
 await waitFor(() => expect(screen.getByLabelText('Default verbosity')).toHaveValue('terse'))
 fireEvent.change(screen.getByLabelText('Default verbosity'), {target:{value:'narrated'}})
 await waitFor(() => expect(apiFetch).toHaveBeenLastCalledWith('/api/me/preferences', expect.objectContaining({method:'PUT',body:'{"verbosity":"narrated"}'})))
})
it('keeps the previous session choice on failure and shows an error', async () => {
 vi.mocked(apiFetch).mockResolvedValue(new Response('{}',{status:503}))
 render(<SessionVerbosity id="session-1" value="standard" />)
 fireEvent.change(screen.getByLabelText('Verbosity'), {target:{value:'terse'}})
 expect(await screen.findByRole('alert')).toHaveTextContent('Could not save verbosity')
 expect(screen.getByLabelText('Verbosity')).toHaveValue('standard')
})
