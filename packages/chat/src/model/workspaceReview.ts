import { viewEpoch } from './viewCache'
import type { ReviewFile, ReviewRequest } from './review'
import type { TransportFiles } from '../transport/types'

export const WORKSPACE_REVIEW_NAME = 'Workspace files'
export interface WorkspaceReviewState {
  requests: ReviewRequest[]
  pending: ReadonlySet<string>
  sending: boolean
  notice: string
}
let nextStore = 0
/** One session's batch, shared by split panes and kept when a workspace tab closes. */
export class WorkspaceReviewStore {
  readonly key = ++nextStore
  private state: WorkspaceReviewState = {requests:[], pending:new Set(), sending:false, notice:''}
  private listeners = new Set<() => void>()
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  snapshot = () => this.state
  update(change: Partial<WorkspaceReviewState>) {
    this.state = {...this.state, ...change}
    for (const listener of this.listeners) listener()
  }
  merge(review: ReviewFile) {
    const local = new Map(this.state.requests.map(r => [r.id, r]))
    for (const r of review.requests) if (r.anchor.workspace && !this.state.pending.has(r.id)) local.set(r.id, r)
    this.update({requests:[...local.values()]})
  }
  add(request: ReviewRequest) {
    this.update({requests:[...this.state.requests, request], pending:new Set([...this.state.pending, request.id]), notice:''})
  }
  remove(id: string) {
    if (!this.state.pending.has(id) || this.state.sending) return
    this.update({requests:this.state.requests.filter(r => r.id !== id), pending:new Set([...this.state.pending].filter(key => key !== id))})
  }
}
let epoch = viewEpoch()
let scopes = new WeakMap<TransportFiles, Map<string, WorkspaceReviewStore>>()
export function workspaceReviewStore(files: TransportFiles, session: string): WorkspaceReviewStore {
  if (epoch !== viewEpoch()) { epoch = viewEpoch(); scopes = new WeakMap() }
  let sessions = scopes.get(files)
  if (!sessions) { sessions = new Map(); scopes.set(files, sessions) }
  const key = JSON.stringify([files.queueScope?.() ?? '', session])
  let store = sessions.get(key)
  if (!store) { store = new WorkspaceReviewStore(); sessions.set(key, store) }
  return store
}
