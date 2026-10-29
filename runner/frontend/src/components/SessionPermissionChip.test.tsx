import { beforeEach, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { useTranscriptStore } from '@blerglab/chat'
import SessionPermissionChip from './SessionPermissionChip'
import { makeSession } from '../test/fixtures'

beforeEach(() => { useTranscriptStore.setState({ sessions: {} }) })

it.each(['danger-full-access', 'workspace-write', 'read-only'])('shows reported %s scope in the session header', async sandbox => {
  useTranscriptStore.getState().ingest({ type: 'agent_event', session_id: 'sess-a', client_event_id: 'caps1', seq: 1, ts: '2026-10-25T00:00:00Z', kind: 'capabilities', payload: { engine: 'codex', permission_mode: 'never', sandbox_mode: sandbox, groups: [] } })
  render(<SessionPermissionChip session={makeSession({ kind: 'agent', engine: 'codex' })} />)
  const chip = await screen.findByTitle(`No prompts · ${{ 'danger-full-access': 'full access', 'workspace-write': 'workspace only', 'read-only': 'read only' }[sandbox]}. Codex: approval never, sandbox ${sandbox}. Runtime confinement still applies.`)
  expect(chip).toHaveTextContent(/^No prompts$/)
  expect(chip).toHaveAttribute('aria-description', chip.getAttribute('title'))
})

it('keeps unknown scope explicit while an older report loads', () => {
  render(<SessionPermissionChip session={makeSession({ kind: 'agent', engine: 'codex' })} />)
  expect(screen.getByTestId('session-permission')).toHaveAttribute('title', expect.stringContaining('scope not reported'))
})
