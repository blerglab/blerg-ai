// Owner review through the shipped app shell, with realistic API and transcript fixtures.
import { afterEach, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import App from '../App'
import { useSessionStore } from '../hooks/useSessionStore'
import { useMessageStore } from '../hooks/useMessageStore'
import { useTranscriptStore } from '@blerglab/chat'
import { makeDaemon, makeSession } from '../test/fixtures'
import { reelReply } from '../test/reelFixture'
import { setTheme } from '../../../../packages/settings/theme'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async orig => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok' }))
vi.mock('../claims', async orig => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))
afterEach(() => { invalidateChatTransport(); window.history.replaceState({}, '', '/'); vi.unstubAllGlobals(); localStorage.clear() })

it.each([390, 3840])('keeps notices, questions, History and file actions clear in the app at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  localStorage.clear()
  setTheme('dark')
  const baseline = import.meta.env.VITE_BLERG_432_BEFORE === '1'
  const lastSeq = baseline ? 4 : 5
  const shots = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
  const now = new Date().toISOString()
  const body = 'The desktop restarted. Work is retained. Check its work before resuming.'
  const artifact = { id: 'release', name: 'release-notes.md', view: 'markdown', content_type: 'text/markdown', size: 120, version: 1 }
  const reply = reelReply()
  reply.topics[0].title = 'Prepare release notes'
  reply.topics[1].title = 'Check recovery wording'
  reply.topics[0].items.push({ type: 'rename', seq: 999, event_id: 'rename', ts: now, rename: { from: 'Worker', to: 'Check release notes', by: 'parent' } })
  const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })
  apiFetch.mockImplementation(async (url: string) => {
    if (url === '/api/session-boards') return json({ configured: true, boards: [] })
    if (url.endsWith('/topics')) return json(reply)
    if (url.startsWith('/api/models')) return json({ models: [{ id: 'release-model', name: 'Release model', section: 'main', efforts: ['low', 'high'], default_effort: 'low' }], source: 'live' })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] })
    if (url.endsWith('/child-approval')) return json({ revision: 'review', daemon_id: 'd1', allowed_engines: ['codex'], runtimes: ['docker', 'daemon'].map(runtime => ({ runtime, enabled: false, partial: false, allowed_engines: ['codex'], granted_engines: [], standing_rule_engines: [], effective_allowed_engines: [], can_enable: true, reason: '' })) })
    if (url.endsWith('/live')) return json({ session_id: 'sess-a', through_seq: lastSeq, generated_at: now, asks: [{ event_id: 'review-4', message_id: 'real-question', question: 'Publish these release notes after review?', asked_at: now, seq: 4, no_wait: true, owner_only: true }], children: [], files: [artifact], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 1, running: 0 }, can_release_prompt: true })
    if (url.includes('/proposals')) return json({ proposals: [], count: 0 })
    if (url.includes('/messages') || url.includes('/boards') || url.includes('/crons')) return json([])
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (String(url).endsWith('/artifacts')) return json({ artifacts: [artifact] })
    if (String(url).includes('/raw')) return new Response('# Release notes\n\nRecovery notices stay in the conversation. Real questions keep their reply control.')
    return json([])
  }))
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', title: 'Prepare the release', engine: 'codex', runtime: 'daemon', started_at: new Date(Date.now() - 30 * 60 * 1000).toISOString() }), makeSession({ id: 'worker', kind: 'agent', runtime: 'daemon', title: 'Check release notes', parent_session_id: 'sess-a', started_by: { kind: 'session' }, paused_reason: 'restart', status: 'waiting' })], statusChangedAt: {}, serverVersion: 'test' })
  useMessageStore.setState({ messages: [{ id: 'failure', session_id: 'worker', kind: baseline ? 'ask' : 'update', status: baseline ? 'open' : 'answered', body, created_at: now, answerable: baseline }, { id: 'real-question', session_id: 'sess-a', kind: 'ask', status: 'open', body: 'Publish these release notes after review?', created_at: now, answerable: true }] })
  useTranscriptStore.getState().resetEvents('sess-a')
  for (const [seq, kind, payload] of [[1, 'user_message', { text: 'Prepare the release notes and check the recovery wording.' }], [2, 'assistant_text', { text: 'The notes are ready for review.' }], [3, 'artifact', artifact], [4, 'ask', { body: 'Publish these release notes after review?', message_id: 'real-question', no_wait: true, owner_only: true }], [5, 'update', { body, message_id: 'failure' }]] as const) {
    if (seq > lastSeq) continue
    useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: `review-${seq}`, seq, kind, payload, ts: now })
  }
  useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq, hasMore: false })
  window.history.replaceState({}, '', '/sessions/sess-a')
  render(<App />)
  await screen.findByText('The notes are ready for review.')
  const shot = async (surface: string) => { if (shots) await page.screenshot({ path: `${shots}/${baseline ? 'before' : 'after'}-${surface}-${width}.png` }) }
  await shot('conversation')
  // Open the published file from its actual transcript card.
  await userEvent.click(await screen.findByRole('button', { name: 'Open release-notes.md' }))
  const region = await screen.findByRole('region', { name: 'release-notes.md' })
  await within(region).findByRole('heading', { name: 'Release notes' })
  await shot('file')
  if (!baseline) expect(within(region).queryByRole('heading', { name: 'release-notes.md' })).toBeNull()
  // Return to Main using the real tab/navigation controls.
  if (width === 390) fireEvent.click(screen.getByRole('button', { name: '← Main' }))
  else fireEvent.click(screen.getByRole('tab', { name: /^Main/ }))
  if (width === 390) fireEvent.click(screen.getByTestId('rail-toggle'))
  const history = screen.getAllByRole('tab', { name: /History/ })[0]
  fireEvent.click(history)
  await screen.findByText(/Worker → Check release notes/)
  await shot('history')
  if (!baseline) await waitFor(() => expect(screen.queryByText(/Renamed by the parent/)).toBeNull())
  if (width === 390) await userEvent.keyboard('{Escape}')
  await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
  await userEvent.click(await screen.findByRole('tab', { name: /Questions/ }))
  const queue = await screen.findByRole('region', { name: 'Question queue' })
  expect(within(queue).getByText('Publish these release notes after review?')).toBeVisible()
  if (!baseline) expect(within(queue).queryByText(body)).toBeNull()
  await shot('questions')
  if (width === 390) await userEvent.keyboard('{Escape}')
  if (width === 390) fireEvent.click(screen.getByTestId('rail-toggle'))
  fireEvent.click(screen.getAllByRole('tab', { name: /^Live/ })[0])
  await userEvent.click(screen.getByRole('button', { name: 'Manage' }))
  await screen.findByRole('dialog', { name: /Permissions/ })
  await shot('permissions')
  await userEvent.keyboard('{Escape}')
  if (width === 390) await userEvent.keyboard('{Escape}')
  await userEvent.keyboard('{Control>}k{/Control}')
  const palette = await screen.findByRole('dialog', { name: 'Command palette' })
  const search = within(palette).getByRole('combobox')
  await userEvent.type(search, 'Discard workspace')
  await userEvent.keyboard('{ArrowDown}{Enter}')
  await screen.findByRole('alertdialog')
  await shot('discard')
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  if (width === 390) await userEvent.keyboard('{Escape}')
  useSessionStore.setState(state => ({ sessions: state.sessions.map(session => session.id === 'sess-a' ? { ...session, status: 'waiting', paused_reason: 'restart' } : session) }))
  useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'restart', seq: lastSeq + 1, kind: 'user_message', payload: { source: 'system', text: '[system] ↻ Host restarted · paused — check the interrupted command' }, ts: now })
  await waitFor(() => expect(screen.getByRole('button', { name: 'Resume' })).toBeEnabled())
  await shot('resume')
})
