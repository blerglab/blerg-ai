import { beforeEach, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { HovercardProvider } from '@blerglab/chat'
import RepositoryRequest from './RepositoryRequest'
import { makeSession } from '../test/fixtures'
import { useSessionStore } from '../hooks/useSessionStore'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
const request = { id: 'operation', kind: 'add' as const, member_name: 'Web', proposed_digest: 'proposal' }
beforeEach(() => { apiFetch.mockReset(); useSessionStore.setState({ sessions: [makeSession({ repository_request: request })] }) })
it.each([true, false])('decides the journal from a question dialog and resolves the chat card: %s', async allow => {
 apiFetch.mockResolvedValue(new Response('{}'))
 render(<HovercardProvider value={{}}><RepositoryRequest sessionId="sess-a" /><RepositoryRequest sessionId="sess-a" card /></HovercardProvider>)
 fireEvent.click(screen.getByRole('button', { name: /Add Web/ }))
 const dialog = screen.getByRole('dialog')
 const buttons = dialog.querySelectorAll('button')
 const button = [...buttons].find(b => b.textContent === (allow ? 'Approve' : 'Decline'))!
 fireEvent.click(button)
 await waitFor(() => expect(screen.queryByRole('group', { name: 'Repository approval' })).toBeNull())
 expect(apiFetch).toHaveBeenCalledWith('/api/sessions/sess-a/workspace/repositories/operation/decide', expect.objectContaining({ body: JSON.stringify({ allow, proposed_digest: 'proposal' }) }))
})
it('keeps failed requests visible and retryable', async () => {
 apiFetch.mockResolvedValue(new Response('{"error":"Try again."}', { status: 503 }))
 render(<RepositoryRequest sessionId="sess-a" card />)
 fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
 expect(await screen.findByRole('alert')).toHaveTextContent('Try again.')
 expect(screen.getByRole('button', { name: 'Decline' })).toBeEnabled()
 expect(useSessionStore.getState().sessions[0].repository_request).toEqual(request)
})
it('approves from chat and removes the question row', async () => {
 apiFetch.mockResolvedValue(new Response('{}'))
 render(<HovercardProvider value={{}}><RepositoryRequest sessionId="sess-a" /><RepositoryRequest sessionId="sess-a" card /></HovercardProvider>)
 fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
 await waitFor(() => expect(screen.queryByRole('button', { name: /Add Web/ })).toBeNull())
 expect(apiFetch).toHaveBeenCalledWith('/api/sessions/sess-a/workspace/repositories/operation/decide', expect.objectContaining({ body: '{"allow":true,"proposed_digest":"proposal"}' }))
})
it('removes a settled clone failure from chat and question surfaces', () => {
 const refusal = 'Could not clone this repository. Check your Git access and available disk space, then try again.'
 useSessionStore.setState({ sessions: [makeSession({ repository_request: { ...request, refusal } })] })
 render(<HovercardProvider value={{}}><RepositoryRequest sessionId="sess-a" /><RepositoryRequest sessionId="sess-a" card /></HovercardProvider>)
 expect(screen.queryByRole('button', { name: /Add Web/ })).toBeNull()
 expect(screen.queryByRole('alert')).toBeNull()
 expect(screen.queryByRole('dialog')).toBeNull()
 expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
 expect(screen.queryByText('Needs you')).toBeNull()
})
it('shows repository names as literal data in the approval title', () => {
 useSessionStore.setState({ sessions: [makeSession({ repository_request: { ...request, member_name: 'github.com/example-org/_repo_' } })] })
 render(<HovercardProvider value={{}}><RepositoryRequest sessionId="sess-a" /></HovercardProvider>)
 fireEvent.click(screen.getByRole('button', { name: /Add github.com\/example-org\/_repo_/ }))
 expect(screen.getByRole('heading', { name: 'Add github.com/example-org/_repo_?' })).toBeTruthy()
})
