// TDD: board API client tests.
// Run: cd frontend && npm test -- src/lib/boardApi.test.ts

import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  listBoards,
  createBoard,
  getBoard,
  deleteBoard,
  createTicket,
  updateTicket,
  moveTicket,
  splitTicket,
  archiveTicket,
  addDependency,
  removeDependency,
  addColumn,
  patchColumn,
  deleteColumn,
  spawnAssist,
} from './boardApi'

// ── fetch mock helpers ─────────────────────────────────────────────────────────

function mockFetch(status: number, body: unknown): void {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValueOnce({
      ok: status >= 200 && status < 300,
      status,
      statusText: 'OK',
      json: async () => body,
    }),
  )
}

function mockFetchError(status: number, errorMsg: string): void {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValueOnce({
      ok: false,
      status,
      statusText: 'Bad Request',
      json: async () => ({ error: errorMsg }),
    }),
  )
}

function lastCall(): [string, RequestInit] {
  const mock = vi.mocked(fetch)
  const call = mock.mock.calls[mock.mock.calls.length - 1]
  return [call[0] as string, call[1] as RequestInit]
}

function lastBody(): Record<string, unknown> {
  const [, init] = lastCall()
  return JSON.parse(init.body as string) as Record<string, unknown>
}

beforeEach(() => {
  vi.restoreAllMocks()
})

// ── GET /api/boards ────────────────────────────────────────────────────────────

describe('listBoards', () => {
  it('GETs /api/boards and returns the parsed array', async () => {
    const boards = [{ id: 'b1', name: 'Sprint' }]
    mockFetch(200, boards)
    const result = await listBoards()
    const [url, init] = lastCall()
    expect(url).toBe('/api/boards')
    expect(init.method).toBe('GET')
    expect(result).toEqual(boards)
  })

  it('throws with the server error message on failure', async () => {
    mockFetchError(500, 'query failed')
    await expect(listBoards()).rejects.toThrow('query failed')
  })
})

// ── POST /api/boards ───────────────────────────────────────────────────────────

describe('createBoard', () => {
  it('POSTs /api/boards with name, repos, and op_id', async () => {
    mockFetch(201, { id: 'b1', name: 'Sprint', columns: [] })
    await createBoard({ name: 'Sprint', repos: ['widget'], opId: 'op-abc' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/boards')
    expect(init.method).toBe('POST')
    const body = lastBody()
    expect(body.name).toBe('Sprint')
    expect(body.repos).toEqual(['widget'])
    expect(body.op_id).toBe('op-abc')
  })

  it('sends a randomly generated op_id when none is provided', async () => {
    mockFetch(201, { id: 'b1', name: 'Sprint', columns: [] })
    await createBoard({ name: 'Sprint', repos: ['widget'] })
    const body = lastBody()
    expect(typeof body.op_id).toBe('string')
    expect((body.op_id as string).length).toBeGreaterThan(0)
  })
})

// ── GET /api/boards/{id} ───────────────────────────────────────────────────────

describe('getBoard', () => {
  it('GETs /api/boards/b1 and returns board with columns and tickets', async () => {
    const data = { id: 'b1', columns: [], tickets: [] }
    mockFetch(200, data)
    const result = await getBoard('b1')
    const [url] = lastCall()
    expect(url).toBe('/api/boards/b1')
    expect(result).toEqual(data)
  })
})

// ── DELETE /api/boards/{id} ───────────────────────────────────────────────────

describe('deleteBoard', () => {
  it('DELETEs /api/boards/b1 with no body', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce({ ok: true, status: 204, json: async () => null }),
    )
    await deleteBoard('b1')
    const [url, init] = lastCall()
    expect(url).toBe('/api/boards/b1')
    expect(init.method).toBe('DELETE')
  })
})

// ── POST /api/boards/{id}/tickets ─────────────────────────────────────────────

describe('createTicket', () => {
  it('POSTs /api/boards/b1/tickets with title and op_id', async () => {
    mockFetch(201, { id: 't1' })
    await createTicket('b1', { title: 'Fix bug', opId: 'op-xyz' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/boards/b1/tickets')
    expect(init.method).toBe('POST')
    const body = lastBody()
    expect(body.title).toBe('Fix bug')
    expect(body.op_id).toBe('op-xyz')
  })
})

// ── PATCH /api/tickets/{id} ────────────────────────────────────────────────────

describe('updateTicket', () => {
  it('PATCHes /api/tickets/t1 with fields and op_id', async () => {
    mockFetch(200, { id: 't1' })
    await updateTicket('t1', { title: 'Renamed', priority: 'high', opId: 'op-upd' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/tickets/t1')
    expect(init.method).toBe('PATCH')
    const body = lastBody()
    expect(body.title).toBe('Renamed')
    expect(body.priority).toBe('high')
    expect(body.op_id).toBe('op-upd')
  })
})

// ── moveTicket (PATCH with column_id) ─────────────────────────────────────────

describe('moveTicket', () => {
  it('PATCHes /api/tickets/t1 with column_id, after, and op_id', async () => {
    mockFetch(200, { id: 't1' })
    await moveTicket('t1', { columnId: 'col-2', after: 't0', opId: 'op-mv' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/tickets/t1')
    expect(init.method).toBe('PATCH')
    const body = lastBody()
    expect(body.column_id).toBe('col-2')
    expect(body.after).toBe('t0')
    expect(body.op_id).toBe('op-mv')
  })
})

// ── POST /api/tickets/{id}/split ──────────────────────────────────────────────

describe('splitTicket', () => {
  it('POSTs /api/tickets/t1/split with titles and op_id', async () => {
    mockFetch(201, [{ id: 't2' }, { id: 't3' }])
    await splitTicket('t1', { titles: ['Part A', 'Part B'], opId: 'op-sp' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/tickets/t1/split')
    expect(init.method).toBe('POST')
    const body = lastBody()
    expect(body.titles).toEqual(['Part A', 'Part B'])
    expect(body.op_id).toBe('op-sp')
  })
})

// ── POST /api/tickets/{id}/archive ────────────────────────────────────────────

describe('archiveTicket', () => {
  it('POSTs /api/tickets/t1/archive with op_id in query string', async () => {
    mockFetch(200, { id: 't1', archived_at: '2026-01-01T00:00:00Z' })
    await archiveTicket('t1', 'op-arc')
    const [url, init] = lastCall()
    expect(url).toBe('/api/tickets/t1/archive?op_id=op-arc')
    expect(init.method).toBe('POST')
  })

  it('omits op_id query param when not given', async () => {
    mockFetch(200, { id: 't1' })
    await archiveTicket('t1')
    const [url] = lastCall()
    expect(url).toBe('/api/tickets/t1/archive')
  })
})

// ── POST /api/tickets/{id}/dependencies ───────────────────────────────────────

describe('addDependency', () => {
  it('POSTs /api/tickets/t1/dependencies with depends_on and op_id', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce({ ok: true, status: 201, json: async () => null }),
    )
    await addDependency('t1', { dependsOnTicketId: 't2', opId: 'op-dep' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/tickets/t1/dependencies')
    expect(init.method).toBe('POST')
    const body = lastBody()
    expect(body.depends_on_ticket_id).toBe('t2')
    expect(body.op_id).toBe('op-dep')
  })
})

// ── DELETE /api/tickets/{id}/dependencies/{depId} ─────────────────────────────

describe('removeDependency', () => {
  it('DELETEs /api/tickets/t1/dependencies/t2 with op_id in query', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce({ ok: true, status: 204, json: async () => null }),
    )
    await removeDependency('t1', 't2', 'op-rd')
    const [url, init] = lastCall()
    expect(url).toBe('/api/tickets/t1/dependencies/t2?op_id=op-rd')
    expect(init.method).toBe('DELETE')
  })
})

// ── POST /api/boards/{id}/columns ─────────────────────────────────────────────

describe('addColumn', () => {
  it('POSTs /api/boards/b1/columns with name and op_id', async () => {
    mockFetch(201, { id: 'c1', name: 'In Review' })
    await addColumn('b1', { name: 'In Review', opId: 'op-col' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/boards/b1/columns')
    expect(init.method).toBe('POST')
    const body = lastBody()
    expect(body.name).toBe('In Review')
    expect(body.op_id).toBe('op-col')
  })
})

// ── PATCH /api/columns/{id} ───────────────────────────────────────────────────

describe('patchColumn', () => {
  it('PATCHes /api/columns/c1 with name and op_id', async () => {
    mockFetch(200, { id: 'c1', name: 'Done' })
    await patchColumn('c1', { name: 'Done', opId: 'op-pc' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/columns/c1')
    expect(init.method).toBe('PATCH')
    const body = lastBody()
    expect(body.name).toBe('Done')
    expect(body.op_id).toBe('op-pc')
  })
})

// ── DELETE /api/columns/{id} ──────────────────────────────────────────────────

describe('deleteColumn', () => {
  it('DELETEs /api/columns/c1 with op_id in query', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce({ ok: true, status: 204, json: async () => null }),
    )
    await deleteColumn('c1', 'op-dc')
    const [url, init] = lastCall()
    expect(url).toBe('/api/columns/c1?op_id=op-dc')
    expect(init.method).toBe('DELETE')
  })
})

// ── POST /api/boards/{id}/assist ──────────────────────────────────────────────

describe('spawnAssist', () => {
  it('POSTs /api/boards/b1/assist with ticket_id and op_id', async () => {
    mockFetch(202, { session_id: 'sess-1' })
    const result = await spawnAssist('b1', { ticketId: 't1', opId: 'op-as' })
    const [url, init] = lastCall()
    expect(url).toBe('/api/boards/b1/assist')
    expect(init.method).toBe('POST')
    const body = lastBody()
    expect(body.ticket_id).toBe('t1')
    expect(body.op_id).toBe('op-as')
    expect(result.session_id).toBe('sess-1')
  })
})

// ── Error handling ─────────────────────────────────────────────────────────────

describe('error handling', () => {
  it('throws with the server error field on a 400 response', async () => {
    mockFetchError(400, 'title is required')
    await expect(createTicket('b1', { title: '' })).rejects.toThrow('title is required')
  })

  it('throws with statusText when the response body has no error field', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce({
        ok: false,
        status: 503,
        statusText: 'Service Unavailable',
        json: async () => ({}),
      }),
    )
    await expect(listBoards()).rejects.toThrow('Service Unavailable')
  })
})
