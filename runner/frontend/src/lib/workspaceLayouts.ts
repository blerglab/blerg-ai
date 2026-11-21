import type { SessionTab } from '../hooks/useSessionTabs'
import type { TabLayout } from './sessionGroups'

export type WorkspaceLayout = { savedAt?: number; mainClosed?: boolean; viewport?: { width: number; height: number }; dock?: { active: string; layout: TabLayout }; tabs: SessionTab[]; layout: TabLayout; active: string; focused: string; panels: Record<string, { open: boolean; width: number }> }
export type NamedLayout = { id: string; name: string; savedAt?: number; layout: WorkspaceLayout }
export type LayoutLibrary = { entries: NamedLayout[]; lastUsed?: string }
export const layoutLibraryKey = (account: string) => `blerg-runner.layouts.${encodeURIComponent(account)}`
// Persist explicit identity fields only. Never copy transcript, chart, ticket or artifact bodies.
export function layoutIdentity(tabs: SessionTab[]): SessionTab[] {
  return tabs.flatMap(tab => {
    const v = tab.view
    const common = { instance: v.instance }
    const view = v.kind === 'insights' || v.kind === 'test-performance' ? { kind: v.kind, ...(v.workspace === undefined ? {} : { workspace: v.workspace }) }
      : v.kind === 'needs-you' ? { kind: v.kind }
      : v.kind === 'inbox' ? { kind: v.kind, itemId: v.itemId }
      : v.kind === 'board' ? { ...common, kind: v.kind, boardId: v.boardId, sessionId: v.sessionId, label: 'Board', copy: v.copy, focusedColumn: undefined }
      : v.kind === 'session-chat' ? { kind: v.kind, sessionId: v.sessionId, label: 'Chat' }
      : v.kind === 'question' ? { kind: v.kind, questionId: v.questionId, sessionId: v.sessionId, label: 'Question' }
      : v.kind === 'shell' ? { kind: v.kind, shellId: v.shellId, runtime: v.runtime, folder: '', label: 'Shell' }
      : v.kind === 'ticket' ? { kind: v.kind, sessionId: v.sessionId, label: v.ticket.key, ticket: { source: 'blerg-board', key: v.ticket.key, board_id: v.ticket.board_id } }
      : v.kind === 'artifact' ? { ...common, kind: v.kind, artifact: { id: v.artifact.id, name: 'File', view: v.artifact.view, size: 0, content_type: '' } }
      : v.kind === 'message' ? { ...common, kind: v.kind, messageId: v.messageId, text: '', label: 'Message' }
      : v.kind === 'visual' ? { ...common, kind: v.kind, id: v.id, visualKind: v.visualKind, source: '', label: 'Visualization' }
      : v.kind === 'workspace' ? { ...common, kind: v.kind, path: v.path }
      : v.kind === 'commit' ? { ...common, kind: v.kind, sha: v.sha, sessionId: v.sessionId, member: v.member }
      : v.kind === 'branch-graph' ? { ...common, kind: v.kind, sessionId: v.sessionId, memberId: v.memberId, label: 'Branches' }
      : v.kind === 'blackboard' ? { ...common, kind: v.kind, blackboardId: v.blackboardId, path: v.path, label: 'Blackboard' }
      : undefined
    return view ? [{ id: tab.id, preview: false, view } as SessionTab] : []
  })
}
export function readLayoutLibrary(account: string): LayoutLibrary {
  try {
    const value = JSON.parse(localStorage.getItem(layoutLibraryKey(account)) ?? 'null')
    return { entries: Array.isArray(value?.entries) ? value.entries.filter((e: NamedLayout) => typeof e.id === 'string' && typeof e.name === 'string' && e.layout && Array.isArray(e.layout.tabs)).slice(0, 50) : [], lastUsed: typeof value?.lastUsed === 'string' ? value.lastUsed : undefined }
  } catch { console.warn('Workspace layouts unavailable: invalid browser storage'); return { entries: [] } }
}
export function writeLayoutLibrary(account: string, library: LayoutLibrary) {
  try { localStorage.setItem(layoutLibraryKey(account), JSON.stringify(library)); return true }
  catch { console.warn('Workspace layouts unavailable: browser storage write failed'); return false }
}
