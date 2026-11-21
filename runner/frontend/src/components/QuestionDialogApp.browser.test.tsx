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


it.each([
  [390, false, 'dark'], [390, true, 'dark'],
  [1024, false, 'dark'], [1024, true, 'dark'],
  [3840, false, 'dark'], [3840, true, 'dark'],
  [390, true, 'colorblind-dark'], [3840, true, 'colorblind-dark'],
] as const)('reads and answers a question at %i px (long %s, %s)', async (width, long, theme) => {
  await page.viewport(width, width === 3840 ? 2160 : 900)
  localStorage.clear()
  setTheme(theme)
  const question = long
    ? 'Which migration should we use?\n\n' + 'Consider existing clients, rollout monitoring, and rollback before choosing. '.repeat(15) + '\n\n- **Option A:** migrate all clients together.\n- **Option B:** keep compatibility while migrating in stages.'
    : 'Ship this change? Check BLG-514.'
  const now = new Date().toISOString()
  const body = 'The desktop restarted. Work is retained. Check its work before resuming.'
  const artifact = { id: 'release', name: 'release-notes.md', view: 'markdown', content_type: 'text/markdown', size: 120, version: 1 }
  const reply = reelReply()
  reply.topics[0].title = 'Prepare release notes'
  reply.topics[1].title = 'Check recovery wording'
  reply.topics[0].items.push({ type: 'rename', seq: 999, event_id: 'rename', ts: now, rename: { from: 'Worker', to: 'Check release notes', by: 'parent' } })
  const json = (value: unknown) => new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })
  apiFetch.mockImplementation(async (url: string) => {
    if (url === '/api/session-boards') return json({ configured: true, boards: [{ id: 'question-board', key: 'BLG' }] })
    if (url.endsWith('/topics')) return json(reply)
    if (url.startsWith('/api/models')) return json({ models: [{ id: 'release-model', name: 'Release model', section: 'main', efforts: ['low', 'high'], default_effort: 'low' }], source: 'live' })
    if (url.startsWith('/api/board-cards/')) return json({ card: { id: 'question-card', board_id: 'question-board', number: 514, reference: 'BLG-514', title: 'Review question references', column_name: 'Ready' } })
    if (url.endsWith('/permissions')) return json({ can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [] })
    if (url.endsWith('/child-approval')) return json({ revision: 'review', daemon_id: 'd1', allowed_engines: ['codex'], runtimes: ['docker', 'daemon'].map(runtime => ({ runtime, enabled: false, partial: false, allowed_engines: ['codex'], granted_engines: [], standing_rule_engines: [], effective_allowed_engines: [], can_enable: true, reason: '' })) })
    if (url.endsWith('/live')) return json({ session_id: 'sess-a', through_seq: 5, generated_at: now, asks: [{ event_id: 'review-4', message_id: 'real-question', question: question, asked_at: now, seq: 4, no_wait: true, owner_only: true }], children: [], files: [artifact], subagents: [], tasks: [], tool_changes: 0, counts: { needs_you: 1, running: 0 }, can_release_prompt: true })
    if (url.includes('/proposals')) return json({ proposals: [], count: 0 })
    if (url.startsWith('/api/messages/real-question/thread')) return json({ question: { id: 'real-question', session_id: 'sess-a', body: question, status: 'open', answerable: true, no_wait: true, owner_only: true, question_turn: 'person' }, entries: [], more: false })
    if (url.includes('/messages') || url.includes('/boards') || url.includes('/crons')) return json([])
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (String(url).endsWith('/artifacts')) return json({ artifacts: [artifact] })
    if (String(url).includes('/raw')) return new Response('# Release notes\n\nRecovery notices stay in the conversation. Real questions keep their reply control.')
    return json([])
  }))
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', title: 'Prepare the release', engine: 'codex', runtime: 'daemon', started_at: new Date(Date.now() - 30 * 60 * 1000).toISOString() }), makeSession({ id: 'worker', kind: 'agent', runtime: 'daemon', title: 'Check release notes', parent_session_id: 'sess-a', started_by: { kind: 'session' }, paused_reason: 'restart', status: 'waiting' })], statusChangedAt: {}, serverVersion: 'test' })
  useMessageStore.setState({ messages: [{ id: 'failure', session_id: 'worker', kind: 'update', status: 'answered', body, created_at: now, answerable: false }, { id: 'real-question', session_id: 'sess-a', kind: 'ask', status: 'open', body: question, created_at: now, answerable: true }] })
  useTranscriptStore.getState().resetEvents('sess-a')
  for (const [seq, kind, payload] of [[1, 'user_message', { text: 'Prepare the release notes and check the recovery wording.' }], [2, 'assistant_text', { text: 'The notes are ready for review.' }], [3, 'artifact', artifact], [4, 'ask', { body: question, message_id: 'real-question', no_wait: true, owner_only: true }], [5, 'update', { body, message_id: 'failure' }]] as const) {
    if (seq > 5) continue
    useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: `review-${seq}`, seq, kind, payload, ts: now })
  }
  useTranscriptStore.getState().ingestReplayDone('sess-a', { lastSeq: 5, hasMore: false })
  window.history.replaceState({}, '', '/sessions/sess-a')
  render(<App />)

  await screen.findByText('The notes are ready for review.')
  if (width === 390) fireEvent.click(screen.getByTestId('rail-toggle'))
  fireEvent.click(screen.getAllByRole('tab', { name: /^Live/ })[0])
  const needs = await screen.findByTestId('rail-needs')
  const row = await within(needs).findByRole('button', { name: /Ship this change|Which migration/ })
  const text = row.querySelector('.question-full-text')!
  row.focus()
  expect(getComputedStyle(text).webkitLineClamp).toBe('none')
  expect(getComputedStyle(text).whiteSpace).toBe('pre-wrap')
  expect(text.scrollHeight).toBeLessThanOrEqual(text.clientHeight + 1)
  expect(text.textContent).toBe(question)
  const shots = import.meta.env.VITE_QUESTION_SHOT_DIR
  const name = `${width}-${long ? 'long' : 'short'}-${theme}`
  const capture = async (surface: string) => {
    if (!shots) return
    // Capture at CSS viewport size rather than the scaled Vitest iframe preview.
    const frame = document.createElement('div')
    frame.style.cssText = 'position:fixed;inset:0;width:100vw;height:100vh;pointer-events:none'
    document.body.append(frame)
    expect(frame.getBoundingClientRect().width).toBe(width)
    try { await page.screenshot({ element: frame, path: `${shots}/${surface}-${name}.png` }) }
    finally { frame.remove() }
  }
  if (long) text.scrollIntoView({ block: 'end' })
  await capture('needs')
  await userEvent.click(row)
  const dialog = screen.getAllByRole('dialog').at(-1)!
  if (!long) expect(dialog.querySelector('.ticket-modal-title')).toHaveTextContent('Ship this change? Check BLG-514.')
  const answer = within(dialog).getByRole('textbox', { name: 'Your answer' })
  expect(answer).toHaveFocus()
  expect(dialog.querySelector('.ticket-modal-title')).toHaveTextContent(question.split('\n')[0])
  expect(getComputedStyle(dialog.querySelector('.ticket-modal-title')!).whiteSpace).toBe('normal') // BLG-575: question titles wrap in full
  expect(within(dialog).getByRole('button', { name: 'Answer' })).toBeDisabled()
  const minimum = answer.getBoundingClientRect().height
  expect(minimum).toBeGreaterThanOrEqual(40)
  await userEvent.fill(answer, 'Use option B.\n' + 'Keep compatibility during rollout.\n'.repeat(12))
  await waitFor(() => expect(answer.getBoundingClientRect().height).toBeGreaterThan(minimum))
  expect(within(dialog).getByRole('button', { name: 'Answer' }).getBoundingClientRect().bottom).toBeLessThan(window.innerHeight)
  const send = within(dialog).getByRole('button', { name: 'Answer' })
  expect(getComputedStyle(send).color).not.toBe(getComputedStyle(send).backgroundColor)
  const close = within(dialog).getByRole('button', { name: 'Close' }).getBoundingClientRect()
  expect(close.top - dialog.getBoundingClientRect().top).toBeLessThan(60)
  expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
  await capture('dialog')
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByTestId('ticket-modal')).toBeNull()
  expect(screen.getByRole('tab', { name: /^Main/ })).toBeVisible()
})
