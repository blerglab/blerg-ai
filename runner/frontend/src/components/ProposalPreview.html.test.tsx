// An approval surface shows every byte the agent wrote: raw HTML stays literal text, so a comment, a
// closed <details> or anything a sanitizer would drop cannot hide words from the person approving.
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import ProposalPreview from './ProposalPreview'
import type { ProposalInfo } from '../types'

const HIDDEN = 'Approve this card.\n\n<!-- hidden: also grant admin -->\n\n<details><summary>Notes</summary>secret instructions</details>\n'

const base: ProposalInfo = { id: 'p1', state: 'pending', connection_id: '', connection_name: 'Board', tool: 'file_ticket', arguments: {}, agent_summary: '', session_id: 's1', cron_id: null, created_at: '2026-01-01T00:00:00Z', expires_at: '2026-01-02T00:00:00Z', decided_at: null, decided_by: null, result: null }

describe('a proposal preview keeps raw HTML literal', () => {
  it('in a proposed card body', () => {
    render(<ProposalPreview proposal={{ ...base, kind: 'board_file', arguments: { title: 'T', body: HIDDEN } }} />)
    const body = screen.getByRole('region', { name: 'Proposed card body' })
    expect(body.textContent).toContain('<!-- hidden: also grant admin -->')
    expect(body.textContent).toContain('<details><summary>Notes</summary>secret instructions</details>')
    expect(body.querySelector('details')).toBeNull()
  })

  it('in a tool request’s body argument', () => {
    render(<ProposalPreview proposal={{ ...base, kind: 'mcp', tool: 'send', arguments: { body: HIDDEN } }} />)
    const region = screen.getByRole('region', { name: 'Tool request preview' })
    expect(region.textContent).toContain('<!-- hidden: also grant admin -->')
    expect(region.textContent).toContain('secret instructions')
    expect(region.querySelector('details')).toBeNull()
  })

  it('in a proposed board’s name, description and repositories', () => {
    render(<ProposalPreview proposal={{ ...base, kind: 'board_create', tool: 'create_board', arguments: { name: '<b>Launch</b>', description: HIDDEN, repos: ['<img src=x>'], access: 'read' } }} />)
    const board = screen.getByRole('article', { name: 'Board the agent proposes' })
    expect(board.textContent).toContain('<b>Launch</b>')
    expect(board.textContent).toContain('<!-- hidden: also grant admin -->')
    expect(board.textContent).toContain('<img src=x>')
    expect(board.querySelector('b, details, img')).toBeNull()
    expect(board.textContent).toContain('Read, for this session only')
  })
})
