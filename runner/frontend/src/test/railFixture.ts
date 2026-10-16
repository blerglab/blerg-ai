// A busy agent session for the side panel's preview and its real-browser test: an open question, a
// subagent at work and one in the background, three background tasks (one fails once the preview
// lets it), and a conversation for the chat beside it; and the server's live state of it
// (GET /api/sessions/{id}/live), which also holds two tasks that started before the events the page
// holds.
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import type { LiveReply } from '../lib/liveState'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from './fixtures'

const MIN = 60_000

export function seedRichRail(sessionId = 'sess-a') {
  const now = Date.now()
  const iso = (ago: number) => new Date(now - ago).toISOString()
  let seq = 0
  const ev = (ago: number, kind: string, payload: unknown): AgentEvent =>
    ({ session_id: sessionId, client_event_id: `rail${++seq}`, seq, ts: iso(ago), kind, payload } as AgentEvent)
  const events: AgentEvent[] = [
    ev(42 * MIN, 'user_message', { text: 'Make a daemon restart keep every agent session alive, and deploy it to staging when the tests pass.', source: 'chat' }),
    ev(41 * MIN, 'assistant_text', { text: 'I will survey the session store first, then run the end-to-end suite and watch the release pipeline in the background.', done: true }),
    ev(30 * MIN, 'tool_call', { tool: 'Agent', call_id: 'A2', input: { description: 'Watch the release pipeline for the first failing stage', prompt: 'Watch', subagent_type: 'general-purpose', run_in_background: true } }),
    ev(30 * MIN, 'subagent_started', { child_id: 'A2', agent_type: 'general-purpose', model: '', summary: '' }),
    ev(30 * MIN, 'subagent_done', { child_id: 'A2', agent_type: 'general-purpose', model: '', summary: 'launched', background: true }),
    ev(30 * MIN, 'tool_result', { call_id: 'A2', output: 'Async agent launched', is_error: false, duration_ms: 1 }),
    ev(30 * MIN, 'background_task_started', { task_id: 'g1', call_id: 'A2', kind: 'agent', description: 'Watch the release pipeline for the first failing stage', started_at: iso(30 * MIN) }),
    ev(29 * MIN, 'subagent_update', { child_id: 'A2', agent_type: 'general-purpose', model: 'claude-sonnet-5', summary: '', usage: { input_tokens: 48_000, output_tokens: 2_100, cache_read_tokens: 0, cache_write_tokens: 0 } }),
    ev(28 * MIN, 'tool_call', { tool: 'Bash', call_id: 'w1', input: { command: 'gh run watch 4412' }, parent_call_id: 'A2' }),
    ev(13 * MIN, 'tool_call', { tool: 'Bash', call_id: 'c1', input: { command: 'npm run test:e2e -- --project chromium', run_in_background: true } }),
    ev(13 * MIN, 'tool_result', { call_id: 'c1', output: 'started', is_error: false, duration_ms: 1 }),
    ev(13 * MIN, 'background_task_started', { task_id: 'b1', call_id: 'c1', kind: 'bash', description: 'npm run test:e2e -- --project chromium', started_at: iso(13 * MIN) }),
    ev(9 * MIN, 'tool_call', { tool: 'Monitor', call_id: 'c2', input: { command: 'tail -f deploy.log', description: 'Watch the deploy log for the first error it reports, then stop' } }),
    ev(9 * MIN, 'tool_result', { call_id: 'c2', output: 'watching', is_error: false, duration_ms: 1 }),
    ev(9 * MIN, 'background_task_started', { task_id: 'm1', call_id: 'c2', kind: 'monitor', description: 'Watch the deploy log for the first error it reports, then stop', started_at: iso(9 * MIN) }),
    ev(8 * MIN, 'tool_call', { tool: 'Bash', call_id: 'c3', input: { command: 'make migrate-staging', run_in_background: true } }),
    ev(8 * MIN, 'tool_result', { call_id: 'c3', output: 'started', is_error: false, duration_ms: 1 }),
    ev(8 * MIN, 'background_task_started', { task_id: 'b2', call_id: 'c3', kind: 'bash', description: 'make migrate-staging', started_at: iso(8 * MIN) }),
    ev(6 * MIN, 'tool_call', { tool: 'Agent', call_id: 'A1', input: { description: 'Survey how the session store persists state', prompt: 'Look around', subagent_type: 'Explore' } }),
    ev(6 * MIN, 'subagent_started', { child_id: 'A1', agent_type: 'Explore', model: '', summary: '' }),
    ev(4 * MIN, 'subagent_update', { child_id: 'A1', agent_type: 'Explore', model: 'claude-haiku-5', summary: '', usage: { input_tokens: 12_000, output_tokens: 400, cache_read_tokens: 0, cache_write_tokens: 0 } }),
    ev(1 * MIN, 'tool_call', { tool: 'Read', call_id: 'k1', input: { file_path: 'runner/internal/store/sessions.go' }, parent_call_id: 'A1' }),
    ev(3 * MIN, 'ask', { body: 'The staging migration touches the sessions table while users are on it. Run it now, or wait for tonight\'s window?', message_id: 'm-ask' }),
  ]
  // A failure the panel keeps until it is dismissed (it ended while the page was open).
  const failed = ev(0, 'background_task_done', { task_id: 'b2', call_id: 'c3', kind: 'bash', status: 'failed', summary: 'Background command "make migrate-staging" failed with exit code 2' })
  useTranscriptStore.setState({ sessions: {} })
  const s = useTranscriptStore.getState()
  for (const e of events) s.ingest(e)
  s.ingestReplayDone(sessionId, { lastSeq: seq, hasMore: false })
  useSessionStore.setState({
    daemons: [makeDaemon()],
    sessions: [makeSession({
      id: sessionId, kind: 'agent', status: 'running', runtime: 'docker', model: 'claude-opus-5-5', effort: 'high',
      title: 'Refactor the session store so a daemon restart keeps every agent session alive',
      started_at: iso(42 * MIN),
      // The server counts the running tasks: the five below and the background subagent's.
      background_tasks: 6,
    })],
    statusChangedAt: {}, serverVersion: 'preview', pendingSessionIds: {}, failedSessions: {},
  })
  // What the server reads from the whole log when the page opens: everything above, and two tasks
  // that started long before the events the page holds.
  const live: LiveReply = {
    session_id: sessionId, through_seq: seq - 1, generated_at: iso(0), tokens_hidden: false,
    asks: [{ event_id: 'rail23', message_id: 'm-ask', question: 'The staging migration touches the sessions table while users are on it. Run it now, or wait for tonight\'s window?', asked_at: iso(3 * MIN), seq: 23 }],
    subagents: [
      { key: 'A2', title: 'Watch the release pipeline for the first failing stage', type: 'general-purpose', model: 'claude-sonnet-5', tokens: 50_100,
        doing: 'running Bash · gh run watch 4412', started_at: iso(30 * MIN), active_at: iso(28 * MIN), background: true, seq: 4 },
      { key: 'A1', title: 'Survey how the session store persists state', type: 'Explore', model: 'claude-haiku-5', tokens: 12_400,
        doing: 'running Read · sessions.go', started_at: iso(6 * MIN), active_at: iso(1 * MIN), background: false, seq: 20 },
    ],
    tasks: [
      { task_id: 'd0', call_id: 'c0', kind: 'bash', description: 'npm run dev -- --port 5173', started_at: iso(3 * 60 * MIN), seq: 0 },
      { task_id: 'm0', call_id: 'c00', kind: 'monitor', description: 'Watch the staging error rate', started_at: iso(2 * 60 * MIN), seq: 0 },
      { task_id: 'b1', call_id: 'c1', kind: 'bash', description: 'npm run test:e2e -- --project chromium', started_at: iso(13 * MIN), seq: 12 },
      { task_id: 'm1', call_id: 'c2', kind: 'monitor', description: 'Watch the deploy log for the first error it reports, then stop', started_at: iso(9 * MIN), seq: 15 },
      { task_id: 'b2', call_id: 'c3', kind: 'bash', description: 'make migrate-staging', started_at: iso(8 * MIN), seq: 18 },
    ],
    tool_changes: 1, counts: { needs_you: 2, running: 7 },
  }
  return { failed, live }
}
