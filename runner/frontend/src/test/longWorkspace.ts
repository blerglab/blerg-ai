import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import { useSessionStore } from '../hooks/useSessionStore'
import { sessionTabsKey } from '../hooks/useSessionTabs'
import { makeSession } from './fixtures'
import { seedRichRail } from './railFixture'
import { richInsights } from './insightsFixture'
import { richActivity } from './activityFixture'
import { RAIL_TAB_KEY } from '../lib/sessionRail'

// Keep the real event history: Transcript windows the DOM, not the store.
export function longWorkspace(account = '', id = 'sess-a') {
  const { live } = seedRichRail(id)
  live.asks = []; live.counts.needs_you = 0
  const workers = Array.from({ length: 5 }, (_, i) => makeSession({ id: `worker-${i}`, title: `Worker ${i}`, parent_session_id: id, started_by: { kind: 'session', name: 'Main' }, kind: 'agent', status: 'running' }))
  // Retained inventory matters independently of the five live workers: this is a synthetic
  // stress size, not a claim about the owner's private inventory.
  const retained = Array.from({ length: 5000 }, (_, i) => makeSession({ id: `retained-${i}`, parent_session_id: id, started_by: { kind: 'session', name: 'Main' }, kind: 'agent', status: 'ended', archived_at: '2026-10-19T12:00:00Z' }))
  useSessionStore.setState(s => ({ sessions: [...s.sessions, makeSession({ id: 'sess-b', kind: 'agent', status: 'idle' }), ...workers, ...retained] }))
  live.children = workers.map(s => ({ id: s.id, title: s.title, engine: 'codex', model: 'model-next', status: s.status, started_at: s.started_at, asking: false, tokens: null }))
  const events = Array.from({ length: 50_000 }, (_, i) => ({ session_id: id, client_event_id: `long-${i}`, seq: i + 100, ts: new Date(Date.UTC(2026, 8, 1) + i * 60_000).toISOString(), kind: i % 2 ? 'assistant_text' : 'user_message', payload: i % 2 ? { text: `Completed task ${i}.\n\nChecked the result and continued.`, done: true } : { text: `Please continue task ${i}`, source: 'chat' } }) as AgentEvent)
  useTranscriptStore.setState({ sessions: {} })
  const store = useTranscriptStore.getState()
  store.ingestMany(id, events)
  store.ingestReplayDone(id, { lastSeq: 50_099, hasMore: false })
  live.through_seq = 50_099
  localStorage.setItem(RAIL_TAB_KEY, 'live')
  localStorage.setItem(sessionTabsKey(account, id), JSON.stringify({ version: 2, active: 'chat', focused: 'main', tabs: [
    { id: 'board:busy', view: { kind: 'board', boardId: 'busy', label: 'Work board' }, preview: false },
    { id: 'insights', view: { kind: 'insights', workspace: id }, preview: false },
  ], layout: { type: 'split', id: 'split', axis: 'horizontal', ratio: .5,
    first: { type: 'group', id: 'main', tabs: ['chat'], active: 'chat' },
    second: { type: 'split', id: 'side', axis: 'vertical', ratio: .5,
      first: { type: 'group', id: 'board', tabs: ['board:busy'], active: 'board:busy' },
      second: { type: 'group', id: 'insights', tabs: ['insights'], active: 'insights' } } } }))
  const board = { columns: [{ id: 'todo', name: 'Todo', rank: 'a' }], releases: [], next_cursor: '', cards: Array.from({ length: 400 }, (_, i) => ({ id: `card-${i}`, board_id: 'busy', number: i + 1, title: `Task ${i + 1}`, column_id: 'todo', rank: String(i).padStart(4, '0'), version: 0 })) }
  return { live, board, permissions: { can_grant: true, reason: '', grants: [], mcp_tools: [], boards: [{ board_id: 'busy', board_name: 'Work board', level: 'full' }] }, insights: richInsights, activity: richActivity }
}

// A second rAF observes the first frame after the input commit had a paint opportunity.
export const painted = () => new Promise<void>((resolve, reject) => {
  const guard = setTimeout(() => reject(new Error(`No paint opportunity (${document.visibilityState})`)), 3000)
  requestAnimationFrame(() => requestAnimationFrame(() => { clearTimeout(guard); resolve() }))
})
