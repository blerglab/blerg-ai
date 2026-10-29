import type { AgentEvent } from '@blerglab/chat'
import { describe, expect, it } from 'vitest'
import { inUseBoards, onlyBoard, unknownBoardTarget, withSessionBoard } from './sessionBoard'

const perms = (boards: { board_id: string; board_name?: string }[]) => ({
  can_grant: true, reason: '', grants: [], mcp_tools: [], boards: boards.map(b => ({ ...b, level: 'full' })),
})

describe('the session\'s one board (#139)', () => {
  it('is the board when there is exactly one, else none', () => {
    expect(onlyBoard(perms([{ board_id: 'b1', board_name: 'Product' }]))).toEqual({ id: 'b1', name: 'Product' })
    expect(onlyBoard(perms([]))).toBeNull()
    expect(onlyBoard(perms([{ board_id: 'b1' }, { board_id: 'b2' }]))).toBeNull()
    expect(onlyBoard({ nonsense: true })).toBeNull()
  })

  it('fills in only a board ticket that names no board', () => {
    const b = { id: 'b1' }
    expect(withSessionBoard({ source: 'blerg-board', key: '#7' }, b)).toEqual({ source: 'blerg-board', key: '#7', boardId: 'b1' })
    expect(withSessionBoard({ source: 'blerg-board', key: '#7', boardId: 'b9' }, b).boardId).toBe('b9')
    expect(withSessionBoard({ source: 'jira', key: 'ABC-1' }, b)).toEqual({ source: 'jira', key: 'ABC-1' })
    expect(withSessionBoard({ source: 'blerg-board', key: '#7' }, null)).toEqual({ source: 'blerg-board', key: '#7' })
  })

  it('says the board is unknown in the hovercard', () => {
    expect(unknownBoardTarget(115)).toEqual({ kind: 'card', source: 'Card', key: '#115', status: 'board unknown' })
  })
})

describe('boards used during a session (#184)', () => {
  const event = (kind: string, payload: unknown, seq = 1) => ({ kind, payload, seq, client_event_id: `e${seq}` } as AgentEvent)
  const pin = (board: string, action = 'pin', seq = 1) => event('ticket', { action, ticket: { source: 'blerg-board', key: '#7', board_id: board, title: 'Work' } }, seq)
  const created = (id: string, access = 'full') => event('proposal_decided', { proposal_id: id, kind: 'board_create', state: 'done', board: { board_id: id, board_name: id, access } })
  it('finds the newly created board even before permission discovery refreshes', () => {
    expect(inUseBoards([created('new-board')])).toEqual([{ id: 'new-board', name: 'new-board' }])
    expect(inUseBoards([created('proposal-only', 'propose')])).toEqual([])
  })
  it('uses live pins and preserves ambiguity across boards', () => {
    expect(inUseBoards([pin('b1')])).toEqual([{ id: 'b1' }])
    expect(inUseBoards([pin('b1'), created('b2')]).map(b => b.id)).toEqual(['b1', 'b2'])
    expect(inUseBoards([pin('b1'), pin('b1', 'unpin', 2)])).toEqual([])
  })
})
