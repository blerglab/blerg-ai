// A hand-built live state for the session panel's Plan tab (its test and its preview).
import type { LiveState, ArtifactInfo } from '@blerglab/chat'

export const planItems = Array.from({ length: 23 }, (_, i) => ({
  text: `Step ${i + 1}: ${['read the module', 'write the failing test', 'implement the change', 'update the docs', 'run the checks'][i % 5]}`,
  done: i < 18,
}))

export const diffFiles: ArtifactInfo[] = [
  { id: 'd2', name: 'second-change.diff', size: 900, content_type: 'text/x-diff', view: 'diff', version: 1, latest_version: 1, created_at: '2026-10-17T11:20:00Z' },
  { id: 'd1', name: 'first-change.diff', size: 700, content_type: 'text/x-diff', view: 'diff', version: 1, latest_version: 1, created_at: '2026-10-17T10:05:00Z' },
  { id: 'n1', name: 'notes.md', size: 100, content_type: 'text/markdown', view: 'markdown', version: 1, latest_version: 1, created_at: '2026-10-17T10:00:00Z' },
]

export function makeLive(over: Partial<LiveState> = {}): LiveState {
  return { settled: true, subagents: [], openAsks: [], backgroundRunning: 0, filesKey: '', clockOffset: 0, heldUntil: null, ...over }
}

export function filesOf(items: ArtifactInfo[]): LiveState['files'] {
  return { ops: {} as never, items, status: 'ready' as never, refresh: () => {} }
}

/** The owner's cluttered list (card #111): finished items sharing one commit and diff, raw worker ids
 *  (a subagent's toolu_… key and a started session's UUID), and open and doing items after them. */
export const SUB_ID = 'toolu_01TJqzn5hgEU53pBcXyZ'
export const CHILD_ID = '7f3c2a10-5b9e-4c1d-8e2f-0a1b2c3d4e5f'
export const SHARED_DIFF = 'e'.repeat(32)
export const clutteredItems = [
  ...Array.from({ length: 13 }, (_, i) => ({
    id: `done-${i}`, text: `Finished step ${i + 1}: a long title that would wrap onto a second and a third line in the panel`, done: true,
    ...(i < 3 ? { commits: ['f2a965d0c1'], diff: SHARED_DIFF } : {}),
    ...(i === 0 ? { assignees: [{ kind: 'subagent' as const, id: SUB_ID }] } : {}),
  })),
  { id: 'doing-1', text: 'Wire the one-line rows', done: false, in_progress: true, assignees: [{ kind: 'subagent' as const, id: SUB_ID }] },
  { id: 'open-1', text: 'Port the parser to the new reader', done: false, assignees: [{ kind: 'child_session' as const, id: CHILD_ID }] },
  { id: 'open-2', text: 'Update the docs', done: false },
]
export const clutteredTitles = { [`subagent:${SUB_ID}`]: 'Audit the plan tab', [`child_session:${CHILD_ID}`]: 'Port the parser' }
