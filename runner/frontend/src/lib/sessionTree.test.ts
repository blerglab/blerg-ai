import { describe, expect, it } from 'vitest'
import { makeSession } from '../test/fixtures'
import { nestChildren, searchTree } from './sessionTree'

const parent = makeSession({ id: 'parent', title: 'Parent', repo: 'app' })
const child = makeSession({ id: 'child', title: 'Review runner', parent_session_id: 'parent', started_at: '2026-10-17T10:00:00Z' })

describe('session tree', () => {
  it('nests a listed child once and orders children by start', () => {
    const earlier = { ...child, id: 'earlier', started_at: '2026-10-17T09:00:00Z' }
    const tree = nestChildren([child, parent, earlier])
    expect(tree.top.map(s => s.id)).toEqual(['parent'])
    expect(tree.childrenOf.get('parent')?.map(s => s.id)).toEqual(['earlier', 'child'])
  })
  it('keeps a child whose parent is not listed at the top', () => {
    expect(nestChildren([child]).top).toEqual([child])
  })
  it('search keeps a parent when a child matches and filters its children', () => {
    const tree = searchTree(nestChildren([parent, child, { ...child, id: 'other', title: 'Other', repo: 'unrelated' }]), 'review')
    expect(tree.top.map(s => s.id)).toEqual(['parent'])
    expect(tree.childrenOf.get('parent')?.map(s => s.id)).toEqual(['child'])
  })
  it('hides archived child cards while preserving the original records for direct navigation', () => {
    const archived = {...child, archived_at:'2026-10-19T12:00:00Z'}
    const source=[parent,archived]
    const tree=nestChildren(source)
    expect(tree.childrenOf.get('parent')??[]).toEqual([])
    expect(tree.top).toEqual([parent])
    expect(source[1]).toBe(archived)
    expect(nestChildren([archived]).top).toEqual([])
  })
  it('never loses malformed self/cyclic lineage', () => {
    expect(nestChildren([{ ...parent, parent_session_id: 'parent' }]).top).toHaveLength(1)
    const cycle = nestChildren([{ ...parent, parent_session_id: 'child' }, child])
    expect(cycle.top).toHaveLength(2)
  })
})
