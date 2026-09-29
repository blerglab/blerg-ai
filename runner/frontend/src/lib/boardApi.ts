// Typed fetch wrappers for the board REST endpoints.
// All mutations include a client-generated op_id (caller may supply their own
// or let the API generate one). Throws on non-2xx with the server's error message.

import type { Board, Column, Ticket, TicketDetail, TicketEvent } from '../types'
import { apiFetch } from '../apiFetch'

// ─── ApiError ─────────────────────────────────────────────────────────────────

/** Thrown by boardFetch on non-2xx responses; carries the HTTP status code. */
export class ApiError extends Error {
  readonly status: number
  constructor(status: number, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

// ─── op_id generation ─────────────────────────────────────────────────────────

/** Generate a short random id suitable for use as an op_id. */
export function newOpId(): string {
  return Math.random().toString(36).slice(2, 10)
}

// ─── core fetch helper ────────────────────────────────────────────────────────

async function boardFetch<T>(
  method: string,
  path: string,
  body?: object,
  extraHeaders?: Record<string, string>,
): Promise<T> {
  const init: RequestInit = { method }
  if (body !== undefined || extraHeaders) {
    init.headers = {
      ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
      ...extraHeaders,
    }
    if (body !== undefined) init.body = JSON.stringify(body)
  }
  const res = await apiFetch(path, init)
  if (!res.ok) {
    let msg = res.statusText
    try {
      const err = (await res.json()) as { error?: string }
      if (err.error) msg = err.error
    } catch {
      // ignore parse error; fall back to statusText
    }
    throw new ApiError(res.status, msg)
  }
  // 204 No Content — nothing to parse
  if (res.status === 204) return undefined as unknown as T
  return res.json() as Promise<T>
}

// ─── Response types ────────────────────────────────────────────────────────────

export type GetBoardResponse = Board & { columns: Column[]; tickets: Ticket[] }
export type CreateBoardResponse = Board & { columns: Column[] }

// ─── Board endpoints ──────────────────────────────────────────────────────────

/** GET /api/boards — list all boards. */
export function listBoards(): Promise<Board[]> {
  return boardFetch<Board[]>('GET', '/api/boards')
}

export interface CreateBoardParams {
  name: string
  repos: string[]
  description?: string | null
  defaultDaemonId?: string | null
  opId?: string
}

/** POST /api/boards — create a board (seeds default columns). */
export function createBoard(params: CreateBoardParams): Promise<CreateBoardResponse> {
  const { name, repos, description, defaultDaemonId, opId } = params
  return boardFetch<CreateBoardResponse>('POST', '/api/boards', {
    name,
    repos,
    description: description ?? null,
    default_daemon_id: defaultDaemonId ?? null,
    op_id: opId ?? newOpId(),
  })
}

/** GET /api/boards/{id} — get a board with its columns and live tickets. */
export function getBoard(id: string): Promise<GetBoardResponse> {
  return boardFetch<GetBoardResponse>('GET', `/api/boards/${id}`)
}

/** DELETE /api/boards/{id} — delete a board and all children (cascade). */
export function deleteBoard(id: string): Promise<void> {
  return boardFetch<void>('DELETE', `/api/boards/${id}`)
}

// ─── Ticket endpoints ─────────────────────────────────────────────────────────

export interface CreateTicketParams {
  title: string
  body?: string | null
  columnId?: string | null
  tags?: string[]
  priority?: string
  size?: string | null
  repos?: string[]
  opId?: string
}

/** POST /api/boards/{boardId}/tickets — create a ticket on a board. */
export function createTicket(boardId: string, params: CreateTicketParams): Promise<Ticket> {
  const { title, body, columnId, tags, priority, size, repos, opId } = params
  return boardFetch<Ticket>('POST', `/api/boards/${boardId}/tickets`, {
    title,
    body: body ?? null,
    column_id: columnId ?? null,
    tags: tags ?? [],
    priority: priority ?? 'medium',
    size: size ?? null,
    repos: repos ?? [],
    op_id: opId ?? newOpId(),
  })
}

export interface UpdateTicketParams {
  title?: string | null
  body?: string | null
  priority?: string | null
  size?: string | null
  addTags?: string[]
  rmTags?: string[]
  addRepos?: string[]
  rmRepos?: string[]
  repos?: string[] | null
  opId?: string
  /** If set, sent as the If-Match header for optimistic concurrency. */
  expectVersion?: number
}

/** PATCH /api/tickets/{id} — update scalar fields of a ticket. */
export function updateTicket(ticketId: string, params: UpdateTicketParams): Promise<Ticket> {
  const { title, body, priority, size, addTags, rmTags, addRepos, rmRepos, repos, opId, expectVersion } = params
  const extraHeaders: Record<string, string> | undefined =
    expectVersion !== undefined ? { 'If-Match': String(expectVersion) } : undefined
  return boardFetch<Ticket>(
    'PATCH',
    `/api/tickets/${ticketId}`,
    {
      ...(title !== undefined ? { title } : {}),
      ...(body !== undefined ? { body } : {}),
      ...(priority !== undefined ? { priority } : {}),
      ...(size !== undefined ? { size } : {}),
      ...(addTags !== undefined ? { add_tags: addTags } : {}),
      ...(rmTags !== undefined ? { rm_tags: rmTags } : {}),
      ...(addRepos !== undefined ? { add_repos: addRepos } : {}),
      ...(rmRepos !== undefined ? { rm_repos: rmRepos } : {}),
      ...(repos !== undefined ? { repos } : {}),
      op_id: opId ?? newOpId(),
    },
    extraHeaders,
  )
}

export interface MoveTicketParams {
  columnId: string
  after?: string | null
  before?: string | null
  opId?: string
}

/** PATCH /api/tickets/{id} — move a ticket to a different column / rank. */
export function moveTicket(ticketId: string, params: MoveTicketParams): Promise<Ticket> {
  const { columnId, after, before, opId } = params
  return boardFetch<Ticket>('PATCH', `/api/tickets/${ticketId}`, {
    column_id: columnId,
    ...(after !== undefined ? { after } : {}),
    ...(before !== undefined ? { before } : {}),
    op_id: opId ?? newOpId(),
  })
}

export interface SplitTicketParams {
  titles: string[]
  opId?: string
}

/** POST /api/tickets/{id}/split — split a ticket into N children (≥2). */
export function splitTicket(ticketId: string, params: SplitTicketParams): Promise<Ticket[]> {
  const { titles, opId } = params
  return boardFetch<Ticket[]>('POST', `/api/tickets/${ticketId}/split`, {
    titles,
    op_id: opId ?? newOpId(),
  })
}

/** POST /api/tickets/{id}/archive — archive a ticket (sets archived_at). */
export function archiveTicket(ticketId: string, opId?: string): Promise<Ticket> {
  const qs = opId ? `?op_id=${encodeURIComponent(opId)}` : ''
  return boardFetch<Ticket>('POST', `/api/tickets/${ticketId}/archive${qs}`)
}

export interface AddDependencyParams {
  dependsOnTicketId: string
  opId?: string
}

/** POST /api/tickets/{id}/dependencies — add a dependency edge. */
export function addDependency(ticketId: string, params: AddDependencyParams): Promise<void> {
  const { dependsOnTicketId, opId } = params
  return boardFetch<void>('POST', `/api/tickets/${ticketId}/dependencies`, {
    depends_on_ticket_id: dependsOnTicketId,
    op_id: opId ?? newOpId(),
  })
}

/** DELETE /api/tickets/{id}/dependencies/{depId} — remove a dependency edge. */
export function removeDependency(ticketId: string, depId: string, opId?: string): Promise<void> {
  const qs = opId ? `?op_id=${encodeURIComponent(opId)}` : ''
  return boardFetch<void>('DELETE', `/api/tickets/${ticketId}/dependencies/${depId}${qs}`)
}

// ─── Column endpoints ─────────────────────────────────────────────────────────

export interface AddColumnParams {
  name: string
  after?: string | null
  isTerminal?: boolean
  opId?: string
}

/** POST /api/boards/{boardId}/columns — create a column. */
export function addColumn(boardId: string, params: AddColumnParams): Promise<Column> {
  const { name, after, isTerminal, opId } = params
  return boardFetch<Column>('POST', `/api/boards/${boardId}/columns`, {
    name,
    ...(after !== undefined ? { after } : {}),
    terminal: isTerminal ?? false,
    op_id: opId ?? newOpId(),
  })
}

export interface PatchColumnParams {
  name?: string | null
  after?: string | null
  before?: string | null
  isTerminal?: boolean | null
  opId?: string
}

/** PATCH /api/columns/{id} — rename / reorder / set terminal flag. */
export function patchColumn(columnId: string, params: PatchColumnParams): Promise<Column> {
  const { name, after, before, isTerminal, opId } = params
  return boardFetch<Column>('PATCH', `/api/columns/${columnId}`, {
    ...(name !== undefined ? { name } : {}),
    ...(after !== undefined ? { after } : {}),
    ...(before !== undefined ? { before } : {}),
    ...(isTerminal !== undefined ? { is_terminal: isTerminal } : {}),
    op_id: opId ?? newOpId(),
  })
}

/** DELETE /api/columns/{id} — delete a column (must be empty). */
export function deleteColumn(columnId: string, opId?: string): Promise<void> {
  const qs = opId ? `?op_id=${encodeURIComponent(opId)}` : ''
  return boardFetch<void>('DELETE', `/api/columns/${columnId}${qs}`)
}

// ─── Assist endpoint ──────────────────────────────────────────────────────────

export interface SpawnAssistParams {
  ticketId?: string
  opId?: string
}

/** POST /api/boards/{boardId}/assist — spawn a board-scoped Assist session. */
export function spawnAssist(
  boardId: string,
  params: SpawnAssistParams = {},
): Promise<{ session_id: string }> {
  const { ticketId, opId } = params
  return boardFetch<{ session_id: string }>('POST', `/api/boards/${boardId}/assist`, {
    ticket_id: ticketId ?? '',
    op_id: opId ?? newOpId(),
  })
}

// ─── Ticket detail endpoint ───────────────────────────────────────────────────

/** GET /api/tickets/{id} — full ticket detail incl. depends_on, blocks, tags, repos. */
export function getTicket(id: string): Promise<TicketDetail> {
  return boardFetch<TicketDetail>('GET', `/api/tickets/${id}`)
}

// ─── Ticket events endpoint ───────────────────────────────────────────────────

export interface ListTicketEventsParams {
  limit?: number
  cursor?: string
}

export interface ListTicketEventsResponse {
  events: TicketEvent[]
  next_cursor: string
}

/** GET /api/tickets/{id}/events — paginated activity feed, newest first. */
export function listTicketEvents(
  id: string,
  params: ListTicketEventsParams = {},
): Promise<ListTicketEventsResponse> {
  const qs = new URLSearchParams()
  if (params.limit !== undefined) qs.set('limit', String(params.limit))
  if (params.cursor) qs.set('cursor', params.cursor)
  const query = qs.toString() ? `?${qs.toString()}` : ''
  return boardFetch<ListTicketEventsResponse>('GET', `/api/tickets/${id}/events${query}`)
}

// ─── Board archive endpoint ───────────────────────────────────────────────────

export interface GetBoardArchiveParams {
  limit?: number
  cursor?: string
}

export interface GetBoardArchiveResponse {
  tickets: Ticket[]
  next_cursor: string
}

/** GET /api/boards/{boardId}/archive — paginated archived tickets, newest-archived first. */
export function getBoardArchive(
  boardId: string,
  params: GetBoardArchiveParams = {},
): Promise<GetBoardArchiveResponse> {
  const qs = new URLSearchParams()
  if (params.limit !== undefined) qs.set('limit', String(params.limit))
  if (params.cursor) qs.set('cursor', params.cursor)
  const query = qs.toString() ? `?${qs.toString()}` : ''
  return boardFetch<GetBoardArchiveResponse>('GET', `/api/boards/${boardId}/archive${query}`)
}
