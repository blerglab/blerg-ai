import { describe, it, expect } from 'vitest'
import { sessionLabel } from './notify'
import type { SessionInfo } from './types'

const base: SessionInfo = {
  id: 'bdb9ccfb-0564-43da-a589-14637ea4cc71',
  daemon_id: 'd1',
  status: 'running',
  project_path: '/repos/widget',
  repo: 'widget',
  title: 'Build the widget',
  started_at: '2026-06-13T00:00:00Z',
}

describe('sessionLabel', () => {
  it('prefers title, then repo, then short id', () => {
    expect(sessionLabel(base)).toBe('Build the widget')
    expect(sessionLabel({ ...base, title: '' })).toBe('widget')
    expect(sessionLabel({ ...base, title: '', repo: '' })).toBe('bdb9ccfb')
  })
})
