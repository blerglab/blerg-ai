import type { AgentEvent } from '@blerglab/chat'
import { useEffect, useMemo } from 'react'
import { create } from 'zustand'
import { useParams } from 'react-router-dom'
import { useSessionStore } from './useSessionStore'
import { rootSessionId } from '../lib/sessionTree'
import { apiFetch } from '../apiFetch'
import { getAccessToken } from '../authClient'
import { tokenSubject } from '../claims'
import * as ws from '../ws'
import { useToastStore } from './useToastStore'

export interface InboxItem { id: string; session_id: string; subject: string; body?: string; preview?: string; kind: string; created_at: string; read: boolean }
interface State { removedIds: string[]; account: string; items: InboxItem[]; unread: number; error: string; loading: boolean; next: string }
function createScope() {
 return { store: create<State>(() => ({ removedIds: [], account: '', items: [], unread: 0, error: '', loading: false, next: '' })), revision: 0, readers: 0, unsubscribe: undefined as (() => void) | undefined }
}
const scopes = new Map<string, ReturnType<typeof createScope>>()
function inboxScope(workspace: string) {
 let scope = scopes.get(workspace)
 if (!scope) { scope = createScope(); scopes.set(workspace, scope) }
 return scope
}
export function inboxURL(path: string, workspace: string) {
 return workspace ? `${path}${path.includes('?') ? '&' : '?'}workspace=${encodeURIComponent(workspace)}` : path
}
export function useInboxWorkspace(explicit?: string) {
 const { id } = useParams<{ id: string }>()
 const root = useSessionStore(state => rootSessionId(state.sessions, id))
 return explicit ?? root ?? id ?? ''
}
// The items the visible Inbox tabs are showing (one today; a set so a future split or pinned
// item stays truthful), so the dock list marks the same rows. Empty while none is visible.
const useInboxOpenStore = create<{ ids: string }>(() => ({ ids: '' }))
export function setInboxShowing(ids: readonly string[]) { const next = [...new Set(ids)].sort().join('\0'); if (useInboxOpenStore.getState().ids !== next) useInboxOpenStore.setState({ ids: next }) }
export function useInboxShowing(id: string) { return useInboxOpenStore(state => state.ids !== '' && state.ids.split('\0').includes(id)) }
export async function refreshInbox(workspace = ''): Promise<void> {
  const scope = inboxScope(workspace), useInboxStore = scope.store
  const account = tokenSubject(getAccessToken())
  const request = ++scope.revision
  if (!account) { useInboxStore.setState({ removedIds: [], account: '', items: [], unread: 0, error: '', loading: false, next: '' }); return }
  if (useInboxStore.getState().account !== account) useInboxStore.setState({ removedIds: [], account, items: [], unread: 0, error: '', loading: true, next: '' })
  else useInboxStore.setState({ loading: true })
  try {
    const response = await apiFetch(inboxURL('/api/inbox', workspace))
    if (!response.ok) throw new Error(`HTTP ${response.status}`)
    let data = await response.json() as { items?: InboxItem[]; unread?: number; next?: string }
    if (scope.revision !== request || account !== tokenSubject(getAccessToken())) return
    const items = Array.isArray(data.items) ? data.items : []
    const target = useInboxStore.getState().items.length
    while (data.next && items.length < target) {
      const older = await apiFetch(inboxURL(`/api/inbox?before=${encodeURIComponent(data.next)}`, workspace))
      if (!older.ok) throw new Error(`HTTP ${older.status}`)
      data = await older.json()
      items.push(...(data.items ?? []))
    }
    if (scope.revision !== request || account !== tokenSubject(getAccessToken())) return
    useInboxStore.setState({ account, items, unread: data.unread ?? items.filter(i => !i.read).length, error: '', loading: false, next: data.next ?? '' })
  } catch (cause) {
    console.warn('Inbox refresh failed', { cause: cause instanceof Error ? cause.name : 'unknown' })
    if (scope.revision === request && account === tokenSubject(getAccessToken())) useInboxStore.setState({ error: 'Couldn’t load the inbox. Try again.' })
  } finally {
    if (scope.revision === request) useInboxStore.setState({ loading: false })
  }
}
function useInboxSubscription(workspace: string) {
  useEffect(() => {
    const scope = inboxScope(workspace), useInboxStore = scope.store
    if (scope.readers++ === 0) {
      void refreshInbox(workspace)
      const off = ws.onMessage('inbox_changed', () => { void refreshInbox(workspace) })
      const reconnect = ws.onOpen(() => { void refreshInbox(workspace) })
      const identity = () => { scope.revision++; useInboxStore.setState({ removedIds: [], account: '', items: [], unread: 0, error: '', loading: false, next: '' }); void refreshInbox(workspace) }
      window.addEventListener('blerg-identity-changed', identity)
      scope.unsubscribe = () => { off(); reconnect(); window.removeEventListener('blerg-identity-changed', identity) }
    }
    return () => { if (--scope.readers === 0) { scope.unsubscribe?.(); scope.unsubscribe = undefined } }
  }, [workspace])
}
export function useInbox(explicit?: string) {
  const workspace = useInboxWorkspace(explicit)
  const state = inboxScope(workspace).store()
  useInboxSubscription(workspace)
  const items = useMemo(() => state.items.filter(item => !state.removedIds.includes(item.id)), [state.items, state.removedIds])
  return state.account === tokenSubject(getAccessToken()) ? { ...state, items, unread: Math.max(0, state.unread - state.items.filter(item => !item.read && state.removedIds.includes(item.id)).length) } : { removedIds: [], account: tokenSubject(getAccessToken()), items: [], unread: 0, error: '', loading: true, next: '' }
}
// Badges need only the count: a primitive selector keeps loading/list writes
// from re-rendering the tab bar on every refresh.
export function useInboxUnread() {
  const workspace = useInboxWorkspace(), useInboxStore = inboxScope(workspace).store
  const unread = useInboxStore(state => state.account === tokenSubject(getAccessToken()) ? Math.max(0, state.unread - state.items.filter(item => !item.read && state.removedIds.includes(item.id)).length) : 0)
  useInboxSubscription(workspace)
  return unread
}
export function useInboxAccount() {
  const workspace = useInboxWorkspace(), useInboxStore = inboxScope(workspace).store
  const stored = useInboxStore(state => state.account)
  useInboxSubscription(workspace)
  const current = tokenSubject(getAccessToken())
  return stored === current ? stored : current
}
export async function setInboxRead(id: string, read: boolean, workspace = '') {
  try {
    const response = await apiFetch(inboxURL(`/api/inbox/${encodeURIComponent(id)}/read`, workspace), { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ read }) })
    if (!response.ok) { console.warn('Inbox read state rejected', { status: response.status }); throw new Error(`HTTP ${response.status}`) }
    await refreshInbox(workspace)
  } catch (cause) {
    console.warn('Inbox read state failed', { cause: cause instanceof Error ? cause.name : 'unknown' })
    throw cause
  }
}
export const OPEN_INBOX = 'blerg-open-inbox'
export function openInbox(id?: string) { window.dispatchEvent(new CustomEvent(OPEN_INBOX, { detail: { id } })) }

export async function loadMoreInbox(workspace = '') {
 const scope = inboxScope(workspace), useInboxStore = scope.store
 const state = useInboxStore.getState()
 if (!state.next) return
 const request = ++scope.revision
 useInboxStore.setState({ loading: true })
 try {
  const response = await apiFetch(inboxURL(`/api/inbox?before=${encodeURIComponent(state.next)}`, workspace))
  if (!response.ok) throw new Error(`HTTP ${response.status}`)
  const data = await response.json() as { items: InboxItem[]; unread: number; next: string }
  if (scope.revision !== request || state.account !== tokenSubject(getAccessToken())) return
  useInboxStore.setState(current => ({ items: [...current.items, ...data.items.filter(item => !current.items.some(old => old.id === item.id))], unread: data.unread, next: data.next, loading: false, error: '' }))
 } catch (cause) {
  console.warn('Inbox page failed', { cause: cause instanceof Error ? cause.name : 'unknown' })
  if (scope.revision === request && state.account === tokenSubject(getAccessToken())) useInboxStore.setState({ error: 'Couldn’t load older items. Try again.' })
 } finally {
  if (scope.revision === request) useInboxStore.setState({ loading: false })
 }
}

export function useInboxEvents(sessionId: string): AgentEvent[] {
 const { items } = useInbox()
 return useMemo(() => items.filter(item => item.session_id === sessionId).map(item => ({ type: 'agent_event', session_id: sessionId, client_event_id: `inbox:${item.id}`, ts: item.created_at, kind: 'inbox_item', payload: { id: item.id, subject: item.subject } })), [items, sessionId])
}

// Hide immediately everywhere, including arrival cards. The server write happens after Undo expires.
// Retain removed IDs until identity reset so an already loaded reader cannot redisplay a deleted body.
export function deleteInboxItem(id: string, workspace = '') {
 const useInboxStore = inboxScope(workspace).store
 const state = useInboxStore.getState(), account = tokenSubject(getAccessToken())
 if (state.account !== account || state.removedIds.includes(id)) return
 useInboxStore.setState({ removedIds: [...state.removedIds, id] })
 const clear = () => {
  if (useInboxStore.getState().account === account) useInboxStore.setState(current => ({ removedIds: current.removedIds.filter(value => value !== id) }))
 }
 let undoable = true
 const timer = setTimeout(() => {
  undoable = false
  useToastStore.getState().removeToast(toastId)
  if (tokenSubject(getAccessToken()) !== account) { clear(); return }
  void (async () => {
   try {
    const response = await apiFetch(inboxURL(`/api/inbox/${encodeURIComponent(id)}`, workspace), { method: 'DELETE' })
    if (!response.ok && response.status !== 404) throw new Error(`HTTP ${response.status}`)
    if (useInboxStore.getState().account === account) useInboxStore.setState(current => ({ items: current.items.filter(item => item.id !== id) }))
    await refreshInbox(workspace)
   } catch {
    clear()
    if (tokenSubject(getAccessToken()) === account) useToastStore.getState().addToast({ title: 'Couldn’t delete item', body: 'Try deleting it again.' })
   }
  })()
 }, 5000)
 const toastId = useToastStore.getState().addToast({ title: 'Item deleted', body: '', ttlMs: 5000, action: { label: 'Undo', onClick: () => { if (!undoable) return; undoable = false; clearTimeout(timer); clear() } } })
}
