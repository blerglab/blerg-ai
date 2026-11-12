// Reach every approval surface through the shipped app, in its default dark theme.
import { afterEach, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, within, waitFor } from '@testing-library/react'
import { commands, page, userEvent } from 'vitest/browser'
import App from '../App'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { useTranscriptStore } from '@blerglab/chat'
import { makeDaemon, makeSession } from '../test/fixtures'
import { setTheme } from '../../../../packages/settings/theme'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok' }))
vi.mock('../claims', async orig => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
afterEach(() => { invalidateChatTransport(); window.history.replaceState({}, '', '/'); vi.unstubAllGlobals(); localStorage.clear() })
it.each([390, 3840])('shares repository decisions across the workspace at %i px', async width => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 localStorage.clear(); setTheme('dark')
 const before = import.meta.env.VITE_BLERG_440_BEFORE === '1'
 const shots = import.meta.env.VITE_BLERG_440_SHOTS
 const request = { id: 'repository-operation', kind: 'add' as const, member_name: 'github.com/example-org/Web', proposed_digest: 'proposal' }
 let pending = true
 const now = new Date().toISOString()
 const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })
 apiFetch.mockImplementation(async (url: string) => {
   if (url.endsWith('/decide')) { pending = false; return json({ ok: true }) }
   if (url.endsWith('/workspace/repositories')) return json({ revision: 0, blockers: {}, operation: pending ? { ...request, member_id: 'web', phase: 'requested', requested_by: 'main' } : undefined })
   if (url === '/api/session-boards') return json({ configured: true, boards: [] })
   if (url.endsWith('/topics')) return json({ topics: [], through_seq: 2 })
   if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] })
   if (url.startsWith('/api/models')) return json({ models: [{ id: 'release-model', name: 'Release model', section: 'main', efforts: ['low'], default_effort: 'low' }], source: 'live' })
   if (url.endsWith('/live')) return json({ session_id: 'sess-a', through_seq: 2, generated_at: now, asks: [], children: [], subagents: [], tasks: [], tool_changes: 0, repository_requests: pending && !before ? 1 : 0, counts: { needs_you: pending && !before ? 1 : 0, running: 0 }, can_release_prompt: true })
   if (url.includes('/proposals')) return json({ proposals: [], count: 0 })
   if (url.includes('/messages') || url.includes('/boards') || url.includes('/crons')) return json([])
   return json({})
 })
 vi.stubGlobal('fetch', vi.fn(async () => json([])))
 useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', title: 'Prepare the release', started_at: new Date(Date.now() - 30 * 60 * 1000).toISOString(), model: 'release-model', engine: 'codex', runtime: 'daemon', repository_request: before ? undefined : request, workspace: { members: [{ id: 'api', name: 'API', path: 'api' }] } })], statusChangedAt: {}, serverVersion: 'test' })
 useMessageStore.setState({ messages: [] })
 useTranscriptStore.getState().resetEvents('sess-a')
 for (const [seq, kind, payload] of [[1, 'user_message', { text: 'Prepare the API and website for release.' }], [2, 'assistant_text', { text: 'The API checks pass. I need the Web repository to check the website too.' }]] as const) useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: `release-${seq}`, seq, kind, payload, ts: now })
 useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
 window.history.replaceState({}, '', '/sessions/sess-a'); const host = document.createElement('div'); host.id = 'root'; document.body.append(host); render(<App />, { container: host })
 await screen.findByText('The API checks pass. I need the Web repository to check the website too.')
 const shot = async (surface: string, element: HTMLElement = document.documentElement) => { if (shots) await page.screenshot({ element, path: `${shots}/${before ? 'before' : 'after'}-${surface}-${width}.png` }) }
 if (!before) expect(await screen.findByRole('group', { name: 'Repository approval' })).toBeVisible()
 await shot('chat')
 if (width === 390) await userEvent.click(screen.getByRole('button', { name: 'Back to workspaces' }))
 if (!before) expect((await screen.findAllByTestId('card-attention-count'))[0]).toHaveTextContent('1 needs you')
 await shot('sidebar')
 if (width === 390) {
   await userEvent.click(screen.getByText('Prepare the release'))
   if (!before) await screen.findByRole('group', { name: 'Repository approval' })
 }
 if (width === 390) fireEvent.click(screen.getByTestId('rail-toggle'))
 fireEvent.click(screen.getAllByRole('tab', { name: /^Live/ })[0])
 const needs = await screen.findByTestId('rail-needs')
 if (!before) expect(within(needs).getByRole('button', { name: /Add github.com\/example-org\/Web/ })).toBeVisible()
 await shot('needs-you')
 if (width === 390) await userEvent.keyboard('{Escape}')
 await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
 await userEvent.click(await screen.findByRole('tab', { name: /Questions/ }))
 const queue = await screen.findByRole('region', { name: 'Question queue' })
 await shot('questions')
 if (!before) {
   await userEvent.click(within(queue).getByRole('button', { name: /Add github.com\/example-org\/Web/ }))
   const dialog = await screen.findByRole('dialog', { name: /Add github.com\/example-org\/Web/ })
   expect(within(dialog).getByRole('button', { name: 'Approve' })).toBeVisible()
   const description = within(dialog).getByText('The workspace will change when it is safe. Work in progress keeps running.')
   expect(within(dialog).getByRole('button', { name: 'Approve' }).getBoundingClientRect().top - description.getBoundingClientRect().bottom).toBe(16)
   await shot('question-dialog', dialog.parentElement!)
   if (import.meta.env.VITE_SETTINGS_SHOTS) {
     for (const theme of ['dark', 'colorblind-dark'] as const) {
       setTheme(theme)
       expect(within(dialog).getByRole('button', { name: 'Approve' }).getBoundingClientRect().top - description.getBoundingClientRect().bottom).toBe(16)
       await commands.settingsScreenshot(width, theme)
     }
     setTheme('dark')
   }
   await userEvent.keyboard('{Escape}')
 }
 await userEvent.click(screen.getByRole('tab', { name: /Repositories/ }))
 const repos = await screen.findByRole('region', { name: 'Workspace repositories' })
 await within(repos).findByText('Main asks to add github.com/example-org/Web')
 await shot('repositories')
 if (!before) {
   await userEvent.click(within(repos).getByRole('button', { name: 'Decline' }))
   await waitFor(() => expect(useSessionStore.getState().sessions[0].repository_request).toBeUndefined())
   await userEvent.click(screen.getByRole('tab', { name: /Questions/ }))
   expect(within(queue).queryByText('Add github.com/example-org/Web?')).toBeNull()
   expect(screen.queryByRole('group', { name: 'Repository approval' })).toBeNull()
 }
})
