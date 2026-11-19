// Deterministic data shared by the two real-host browser proofs and screenshots.
export const parityBoard = { id: 'parity', name: 'Release work', repos: ['example-org/example-repo'], field_schema: [], model: '', concurrency: 1 }
export const parityColumns = ['Inbox', 'In progress', 'Review', 'Done'].map((name, i) => ({ id: `lane-${i}`, board_id: 'parity', name, rank: String(i), is_terminal: i === 3 }))
export const parityCards = [
  { id: 'card-1', number: 1, column_id: 'lane-0', title: 'Keep board cards compact with the same shared presentation', type: 'bug', priority: 'urgent', size: 'M', risk: 'low', complexity: 'medium', tags: ['frontend', 'to-test'], stale_at: '2026-01-01T00:00:00Z' },
  { id: 'card-2', number: 2, column_id: 'lane-1', title: 'LongUnbrokenTitle'.repeat(6), type: 'feature', priority: 'high', size: 'S', risk: 'low', complexity: 'low', tags: ['frontend'], stale_at: null },
].map((c, i) => ({ board_id: 'parity', reference: `DEMO-${c.number}`, rank: String(i), body: '', fields: {}, repos: ['example-org/example-repo'], links: [], depends_on: [], blockers: [], model: '', version: 0, auto_merge: false, run_attempts: 0, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', dedup_key: null, external_id: null, archived_at: null, gate_flag: null, merged_sha: null, merged_at: null, stuck_at: null, ...c }))

// Eighty realistic, short tickets for the full-area focus acceptance checks.
const work = ['Keep filters when switching panels', 'Show review notes beside the card', 'Restore the selected release', 'Keep keyboard focus after closing details', 'Refresh status after a move', 'Make repository links easy to find', 'Preserve ticket order on reload', 'Show a clear retry notice']
export const denseCards = Array.from({ length: 80 }, (_, i) => ({
  ...parityCards[0], id: `dense-${i + 1}`, number: i + 1, reference: `REL-${i + 1}`,
  title: work[i % work.length], column_id: 'lane-1', rank: String(i).padStart(3, '0'),
  stale_at: null, priority: 'normal', type: i % 3 ? 'feature' : 'bug',
  size: i % 3 ? 'S' : 'M', risk: null, complexity: null, tags: [],
}))
