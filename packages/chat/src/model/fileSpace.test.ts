import {describe, expect, it} from 'vitest'
import {oldestFiles, selectForSpace, uploadShortfall} from './fileSpace'
import type {FileSpace} from '../transport/types'
const space: FileSpace = {can_delete: true, limits: {max_session_upload_bytes: 10}, artifacts: [
  {id: 'last', name: 'last.txt', size: 5, content_type: 'text/plain', view: 'text', origin: 'user', created_at: '2026-10-03'},
  {id: 'published', name: 'report.md', size: 100, content_type: 'text/plain', view: 'text', origin: 'agent', created_at: '2026-10-01'},
  {id: 'first', name: 'first.txt', size: 2, content_type: 'text/plain', view: 'text', origin: 'user', created_at: '2026-10-02'},
]}
describe('space preselection', () => {
  it('counts only attachments and takes the oldest files until their sum covers the shortfall', () => {
    expect(oldestFiles(space.artifacts).map(a => a.id)).toEqual(['published', 'first', 'last'])
    expect(uploadShortfall(space, 6)).toBe(3)
    expect([...selectForSpace(space, 6)]).toEqual(['first', 'last'])
    expect([...selectForSpace(space, 5)]).toEqual(['first'])
    expect([...selectForSpace(space, 3)]).toEqual([])
  })
  it('reverses the server’s newest-first order for timestamps tied within one second', () => {
    const items = space.artifacts.map(a => ({...a, created_at: '2026-10-03T00:00:00Z'}))
    expect(oldestFiles(items).map(a => a.id)).toEqual(['first', 'published', 'last'])
  })
  it('handles missing quotas, zero-size files, and an already-over-quota session', () => {
    expect([...selectForSpace({...space, limits: {}}, 1000)]).toEqual([])
    expect(uploadShortfall({...space, limits: {max_session_upload_bytes: 5}}, 1)).toBe(3)
    expect([...selectForSpace({...space, artifacts: [{id: 'zero', name: 'empty', origin: 'user', size: 0, content_type: 'text/plain', view: 'text'}, ...space.artifacts]}, 6)]).toEqual(['first', 'last'])
  })
})
