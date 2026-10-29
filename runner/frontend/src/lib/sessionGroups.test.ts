import { expect, it } from 'vitest'
import { groups, normalizeLayout, moveToGroup, resizeSplit, type TabLayout } from './sessionGroups'

const initial: TabLayout = { type: 'group', id: 'main', tabs: ['chat', 'a', 'b'], active: 'a' }
it('splits on all four edges, nests, moves to center and prunes empty groups', () => {
  for (const edge of ['left', 'right', 'top', 'bottom'] as const) {
    const split = moveToGroup(initial, 'a', 'main', edge, 'new')
    expect(split.type).toBe('split')
    if (split.type !== 'split') throw new Error('Expected split')
    expect(split.axis).toBe(['left', 'right'].includes(edge) ? 'vertical' : 'horizontal')
    expect(groups(split).map(g => g.id)).toEqual(['left', 'top'].includes(edge) ? ['new', 'main'] : ['main', 'new'])
    const nested = moveToGroup(split, 'b', 'main', 'bottom', 'nested')
    expect(groups(nested)).toHaveLength(3)
    const merged = moveToGroup(nested, 'b', 'new', 'center', 'unused')
    expect(groups(merged)).toHaveLength(2)
    expect(groups(merged).find(g => g.id === 'new')?.tabs).toEqual(['a', 'b'])
  }
})
it('does not create empty groups, duplicate a tab or accept a foreign drag', () => {
  const lone: TabLayout = { type: 'group', id: 'main', tabs: ['chat'], active: 'chat' }
  expect(moveToGroup(lone, 'chat', 'main', 'right', 'new')).toEqual(lone)
  expect(moveToGroup(initial, 'foreign', 'main', 'right', 'new')).toEqual(initial)
})
it('restores valid identities and ratios, prunes closed views and repairs corrupt storage', () => {
  const split = moveToGroup(initial, 'a', 'main', 'right', 'new')
  if (split.type !== 'split') throw new Error('Expected split')
  const resized = resizeSplit(split, split.id, .7)
  expect(resized).toMatchObject({ ratio: .7 })
  expect(normalizeLayout(resized, ['chat', 'b'], 'chat')).toEqual({ type: 'group', id: 'main', tabs: ['chat', 'b'], active: 'chat' })
  const bad = { type: 'split', first: { type: 'group', id: 'x', tabs: ['chat', 'chat'], active: 'missing' }, second: { type: 'group', id: 'x', tabs: ['a'], active: 'a' } }
  const repaired = groups(normalizeLayout(bad, ['chat', 'a', 'b'], 'b'))
  expect(repaired.flatMap(g => g.tabs).sort()).toEqual(['a', 'b', 'chat'])
  expect(new Set(repaired.map(g => g.id)).size).toBe(repaired.length)
  expect(groups(normalizeLayout({ type: 'split', first: null }, ['chat'], 'chat'))).toHaveLength(1)
})
it('keeps Chat first in its destination so numbered shortcuts and visual tab order agree', () => {
  const split = moveToGroup(initial, 'a', 'main', 'right', 'new')
  const moved = moveToGroup(split, 'chat', 'new', 'center', 'unused')
  expect(groups(moved).find(g => g.id === 'new')?.tabs).toEqual(['chat', 'a'])
})
