import { afterEach, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { page } from 'vitest/browser'
import DelegationCard from '../../../../packages/chat/src/components/cards/DelegationCard'
import type { AgentEvent } from '../../../../packages/chat/src/types'
import '../../../../packages/chat/src/theme/tokens.css'
import '../../../../packages/chat/src/theme/blerg.css'
import '../../../../packages/chat/src/components/chat.css'

afterEach(cleanup)
it.each([390, 3840])('shows selected member paths on the child approval card at %ipx', async width => {
  await page.viewport(width, 900)
  const path = `group/${'nested-folder-'.repeat(30)}/api`
  const event: AgentEvent = { type: 'agent_event', session_id: 'session', client_event_id: 'request', seq: 1, ts: '2026-01-01T00:00:00Z', kind: 'delegation_request', payload: { request_id: 'request', engine: 'codex', runtime: 'docker', prompt: 'Review both repositories', expires_at: '2099-01-01T00:00:00Z', offered: ['once', 'deny'], members: [{ id: 'api', name: 'API', path }, { id: 'web', name: 'Web', path: 'web' }] } }
  render(<div style={{width: '100%', maxWidth: 1200, boxSizing: 'border-box', padding: 16}}><DelegationCard ev={event} /></div>)
  const members = screen.getByRole('list', { name: 'Chosen repositories' })
  expect(members.textContent).toContain(path)
  expect(members.textContent).toContain('Web web')
  expect(members.scrollWidth).toBeLessThanOrEqual(members.clientWidth + 1)
  expect(members.getBoundingClientRect().right).toBeLessThanOrEqual(width)
})
