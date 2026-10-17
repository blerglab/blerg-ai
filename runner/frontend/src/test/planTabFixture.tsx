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
