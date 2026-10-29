// The boards a session is associated with, for the tab bar's board button: its launch board and
// every board it has access to above propose (GET /api/sessions/{id}/permissions). The launch
// board comes first, then by name. The panel's own reads of the same endpoint update it, so a
// grant made there shows here without another request.
import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { readPermissions, type PermissionsView } from './sessionPermissions'

export interface BoardLink { id: string; name: string; launch: boolean }

/** The session's boards, launch board first, one entry per board. Names fall back to `names`,
 *  then to the id. */
export function boardLinksOf(view: PermissionsView, names: Record<string, string> = {}): BoardLink[] {
  const out = new Map<string, BoardLink>()
  for (const b of view.boards) {
    if (!b.board_id || (b.level === 'propose' && !b.launch)) continue
    const had = out.get(b.board_id)
    const name = b.board_name || had?.name || names[b.board_id] || b.board_id
    out.set(b.board_id, { id: b.board_id, name, launch: !!b.launch || !!had?.launch })
  }
  return [...out.values()].sort((x, y) => Number(y.launch) - Number(x.launch) || x.name.localeCompare(y.name) || x.id.localeCompare(y.id))
}

/** The Board app's page of one board. */
export function boardPageUrl(origin: string, boardId: string): string {
  return `${origin}/boards/${encodeURIComponent(boardId)}`
}

const held = new Map<string, BoardLink[]>()
const listeners = new Map<string, Set<(links: BoardLink[]) => void>>()
let names: Promise<Record<string, string>> | undefined
/** Every read (and every panel publication) takes a ticket before it starts: the epoch it began in
 *  and its order. A reply is stored only in the same epoch, and only if no read of the session that
 *  began later has been stored already, so neither another account's late reply nor an older read
 *  replaces what is shown. */
export interface BoardLinksTicket { epoch: number; order: number }
let epoch = 0
let orders = 0
const storedOrder = new Map<string, number>()
export function boardLinksTicket(): BoardLinksTicket { return { epoch, order: ++orders } }
// What one account read is never shown to another (a silent renewal can sign in someone else): the
// reads still out belong to the old epoch and are dropped when they land.
if (typeof window !== 'undefined') window.addEventListener('blerg-identity-changed', () => { held.clear(); storedOrder.clear(); names = undefined; epoch++ })

// Board names for boards the permissions reply names by id only (an untouched launch board).
// Read once per page; a failure is not remembered.
function boardNames(): Promise<Record<string, string>> {
  if (!names) {
    names = apiFetch('/api/session-boards').then(async r => {
      if (!r.ok) throw new Error('boards')
      const body = await r.json() as { boards?: { id?: unknown; name?: unknown }[] }
      return Object.fromEntries((body.boards ?? []).filter(b => typeof b?.id === 'string' && typeof b?.name === 'string').map(b => [b.id as string, b.name as string]))
    }).catch(() => { names = undefined; return {} })
  }
  return names
}

async function linksOf(view: PermissionsView): Promise<BoardLink[]> {
  const links = boardLinksOf(view)
  if (!links.some(l => l.name === l.id)) return links
  return boardLinksOf(view, await boardNames())
}

function store(sessionId: string, links: BoardLink[], ticket: BoardLinksTicket) {
  if (ticket.epoch !== epoch || ticket.order < (storedOrder.get(sessionId) ?? 0)) return
  storedOrder.set(sessionId, ticket.order)
  const before = held.get(sessionId)
  if (before && JSON.stringify(before) === JSON.stringify(links)) return
  held.set(sessionId, links)
  for (const fn of listeners.get(sessionId) ?? []) fn(links)
}

/** The panel read the session's permissions: the board button follows. `ticket`: taken when that
 *  read started (boardLinksTicket), so a reply that lost its place is not published. */
export function publishSessionPermissions(sessionId: string, view: PermissionsView, ticket: BoardLinksTicket = boardLinksTicket()): void {
  void linksOf(view).then(links => store(sessionId, links, ticket))
}

/** The session's boards; empty until read, and when the session has none or they cannot be read. */
export function useSessionBoardLinks(sessionId: string, refreshKey = ''): BoardLink[] {
  const [shown, setShown] = useState<{ sessionId: string; links: BoardLink[] }>(() => ({ sessionId, links: held.get(sessionId) ?? [] }))
  useEffect(() => {
    if (!sessionId) return
    let live = true
    const set = (links: BoardLink[]) => { if (live) setShown({ sessionId, links }) }
    const mine = listeners.get(sessionId) ?? new Set()
    mine.add(set)
    listeners.set(sessionId, mine)
    const ticket = boardLinksTicket()
    const ac = new AbortController()
    void apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/permissions`, { signal: ac.signal })
      .then(async r => {
        if (r.status === 404 || r.status === 403) return []
        if (!r.ok) return null
        return linksOf(readPermissions(await r.json()))
      })
      .catch(() => null)
      .then(links => {
        if (!live) return
        if (links) store(sessionId, links, ticket)
        set(held.get(sessionId) ?? [])
      })
    return () => { live = false; ac.abort(); mine.delete(set) }
  }, [sessionId, refreshKey])
  return shown.sessionId === sessionId ? shown.links : (held.get(sessionId) ?? [])
}

/** For tests: forget what was read. */
export function resetSessionBoardLinksForTest(): void {
  held.clear()
  listeners.clear()
  storedOrder.clear()
  names = undefined
  epoch++
}
