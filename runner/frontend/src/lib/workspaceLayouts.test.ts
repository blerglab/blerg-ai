import { expect, it } from 'vitest'
import { layoutIdentity, readLayoutLibrary, writeLayoutLibrary } from './workspaceLayouts'
it('stores identity without message, chart or ticket bodies', () => {
  const tabs = layoutIdentity([{ id: 'message:m', preview: false, view: { kind: 'message', messageId: 'm', text: 'PRIVATE-CANARY', label: 'PRIVATE-CANARY' } }, { id: 'insights', preview: false, view: { kind: 'insights' } }])
  expect(JSON.stringify(tabs)).not.toContain('PRIVATE-CANARY')
  expect(tabs.map(t => t.id)).toEqual(['message:m', 'insights'])
})
it('keeps saved names and last-used identity within the account', () => {
  const layout = { tabs: [], layout: { type: 'group' as const, id: 'main', tabs: ['chat'], active: 'chat' }, active: 'chat', focused: 'main', panels: {} }
  writeLayoutLibrary('a', { entries: [{ id: 'one', name: 'Review', layout }], lastUsed: 'one' })
  expect(readLayoutLibrary('a').lastUsed).toBe('one')
  expect(readLayoutLibrary('b').entries).toEqual([])
})
