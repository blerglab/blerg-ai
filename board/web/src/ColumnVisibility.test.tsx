import { act, fireEvent, render, renderHook, screen } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { announceBoardMove, HiddenColumnsNotice, useColumnVisibility } from '../../../packages/board/columnVisibility'

const columns = [{ id: 'todo', name: 'Todo' }, { id: 'done', name: 'Done' }]
beforeEach(() => {
  const values = new Map<string, string>()
  vi.stubGlobal('localStorage', { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value), clear: () => values.clear() })
})
afterEach(() => vi.unstubAllGlobals())
it('saves any subset per person and board, with unknown identities isolated and new columns visible', () => {
  localStorage.clear()
  const { result, rerender, unmount } = renderHook(({ person, board }) => useColumnVisibility(person, board, columns), { initialProps: { person: 'person-a', board: 'board-a' } })
  act(() => result.current.toggle('done'))
  expect(result.current.hidden).toEqual(['done'])
  rerender({ person: 'person-b', board: 'board-a' }); expect(result.current.hidden).toEqual([])
  rerender({ person: 'person-a', board: 'board-b' }); expect(result.current.hidden).toEqual([])
  rerender({ person: '', board: 'board-a' }); expect(result.current.hidden).toEqual([])
  rerender({ person: 'person-a', board: 'board-a' }); expect(result.current.hidden).toEqual(['done'])
  unmount()
  const next = renderHook(() => useColumnVisibility('person-a', 'board-a', [...columns, { id: 'new', name: 'New' }]))
  expect(next.result.current.hidden).toEqual(['done'])
})

it('leaves an obvious recovery when every column is hidden and announces moves into hidden columns', () => {
  function View() {
    const visibility = useColumnVisibility('', 'board-notice', columns)
    return <><button onClick={() => { visibility.toggle('todo') }}>Hide Todo</button><button onClick={() => visibility.toggle('done')}>Hide Done</button><HiddenColumnsNotice boardId="board-notice" visibility={visibility} cards={[{ column_id: 'done' }]} /></>
  }
  render(<View />)
  fireEvent.click(screen.getByText('Hide Todo')); fireEvent.click(screen.getByText('Hide Done'))
  expect(screen.getByRole('status')).toHaveTextContent('No columns shown. Hidden: Todo (0), Done (1).')
  act(() => announceBoardMove('board-notice', 'done', '#7'))
  expect(screen.getByRole('status')).toHaveTextContent('#7 moved to hidden column Done.')
  fireEvent.click(screen.getByText('Show all columns'))
  expect(screen.queryByRole('status')).toBeNull()
})

it('synchronizes mounted views and browser storage updates for the same person and board', () => {
  localStorage.clear()
  const a = renderHook(() => useColumnVisibility('same-person', 'same-board', columns))
  const b = renderHook(() => useColumnVisibility('same-person', 'same-board', columns))
  act(() => a.result.current.toggle('done'))
  expect(b.result.current.hidden).toEqual(['done'])
  act(() => { localStorage.clear(); window.dispatchEvent(new StorageEvent('storage', { key: null })) })
  expect(a.result.current.hidden).toEqual([])
  expect(b.result.current.hidden).toEqual([])
})

it('keeps a usable current view when preferences are corrupt or browser storage is blocked', () => {
  vi.stubGlobal('localStorage', { getItem: () => '{bad json', setItem: () => { throw new Error('blocked') } })
  const { result } = renderHook(() => useColumnVisibility('person', 'board', columns))
  expect(result.current.hidden).toEqual([])
  act(() => result.current.toggle('todo'))
  expect(result.current.hidden).toEqual(['todo'])
  act(() => result.current.showAll())
  expect(result.current.hidden).toEqual([])
})

it('never leaks the shared board default or another tab into a fresh tab (BLG-465)', () => {
 const defaults = renderHook(() => useColumnVisibility('person', 'board', columns))
 act(() => defaults.result.current.toggle('done'))
 const first = renderHook(() => useColumnVisibility('person', 'board', columns, 'first-tab'))
 expect(first.result.current.hidden).toEqual([])
 act(() => first.result.current.toggle('todo'))
 const second = renderHook(() => useColumnVisibility('person', 'board', columns, 'second-tab'))
 expect(second.result.current.hidden).toEqual([])
 expect(first.result.current.hidden).toEqual(['todo'])
 first.unmount()
 const restored = renderHook(() => useColumnVisibility('person', 'board', columns, 'first-tab'))
 expect(restored.result.current.hidden).toEqual(['todo'])
})
