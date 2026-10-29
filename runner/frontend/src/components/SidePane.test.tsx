import { beforeEach, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen } from '@testing-library/react'
import SidePane from './SidePane'
import PaneProvider from './PaneProvider'
import { useContext } from 'react'
import { PaneContext, usePaneControl } from '../panes/context'
import { PaletteContext } from '../commands/context'
import { CommandRegistry } from '../commands/registry'
import { panePrefKey, usePaneLayout, usePaneDimensions } from '../hooks/usePaneLayout'
function RestoreButton() {
  const context = useContext(PaneContext)!
  return <button onClick={context.restore}>Restore</button>
}
const account = vi.hoisted(() => ({ current: 'person-a' }))
vi.mock('../lib/liveBoardCard', () => ({ signedInAccount: () => account.current }))
function Harness({ side = 'left' }: { side?: 'left' | 'right' | 'bottom' }) {
  const layout = usePaneLayout('fixture', 'Toggle fixture', { min: 200, max: 600, default: 300 }, true)
  return <SidePane layout={layout} label="Fixture" toggleLabel="Toggle fixture" side={side} icon={<span aria-hidden="true">□</span>}><input aria-label="Content" /></SidePane>
}
beforeEach(() => { localStorage.clear(); account.current = 'person-a' })
it('keeps widths and open states per person and resets to the default', () => {
  const view = render(<Harness />)
  fireEvent.keyDown(screen.getByRole('separator'), { key: 'ArrowRight' })
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '320')
  account.current = 'person-b'; view.rerender(<Harness />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '300')
  fireEvent.click(screen.getByRole('button', { name: 'Collapse fixture' }))
  expect(screen.queryByRole('region')).toBeNull()
  account.current = 'person-a'; view.rerender(<Harness />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '320')
  fireEvent.doubleClick(screen.getByRole('separator'))
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '300')
  expect(JSON.parse(localStorage.getItem(panePrefKey('fixture', 'person-a'))!)).toEqual({ open: true, width: 300 })
  expect(JSON.parse(localStorage.getItem(panePrefKey('fixture', 'person-b'))!)).toEqual({ open: false, width: 300 })
})
it('resizes a future right pane from its inner edge and reopens with keyboard focus', () => {
  render(<Harness side="right" />)
  const divider = screen.getByRole('separator')
  expect(divider).toHaveClass('pane-resize-right')
  fireEvent.keyDown(divider, { key: 'ArrowLeft' })
  expect(divider).toHaveAttribute('aria-valuenow', '320')
  fireEvent.keyDown(divider, { key: 'Home' })
  expect(divider).toHaveAttribute('aria-valuenow', '200')
  fireEvent.click(screen.getByRole('button', { name: 'Collapse fixture' }))
  const toggle = screen.getByRole('button', { name: 'Toggle fixture' })
  expect(document.activeElement).toBe(toggle)
  fireEvent.click(toggle)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '200')
})
it('Focus snapshot does not overwrite another person layout', () => {
  const registry = new CommandRegistry()
  const ui = () => <PaletteContext.Provider value={{ registry, open: () => {} }}><PaneProvider><Harness /><RestoreButton /></PaneProvider></PaletteContext.Provider>
  const view = render(ui())
  act(() => registry.getSnapshot().items.find(x => x.id === 'focus-mode')!.run())
  localStorage.setItem(panePrefKey('fixture', 'person-b'), JSON.stringify({ open: false, width: 450 }))
  account.current = 'person-b'; view.rerender(ui())
  fireEvent.click(screen.getByText('Restore'))
  expect(screen.queryByRole('region', { name: 'Fixture' })).toBeNull()
  expect(JSON.parse(localStorage.getItem(panePrefKey('fixture', 'person-b'))!)).toEqual({ open: false, width: 450 })
})
for (const side of ['left', 'right'] as const) it(`keeps a stable disclosure relationship on the ${side}`, () => {
  render(<Harness side={side} />)
  const id = screen.getByRole('region').id
  const collapse = screen.getByRole('button', { name: 'Collapse fixture' })
  expect(collapse).toHaveAttribute('aria-expanded', 'true')
  expect(collapse).toHaveAttribute('aria-controls', id)
  fireEvent.click(collapse)
  const toggle = screen.getByRole('button', { name: 'Toggle fixture' })
  expect(toggle).toHaveAttribute('aria-expanded', 'false')
  expect(toggle).toHaveAttribute('aria-controls', id)
  fireEvent.click(toggle)
  expect(screen.getByRole('region').id).toBe(id)
})

function PersistentPane({ open, change }: { open: boolean; change(open: boolean): void }) {
  usePaneControl('persistent', 'Toggle persistent', { destructive: false }, open, change)
  return null
}
for (const returnToFirst of [false, true]) it(`discards the entire session snapshot across navigation, return=${returnToFirst}`, () => {
  const registry = new CommandRegistry()
  const change = vi.fn()
  const ui = (scope: string, open: boolean) => <PaletteContext.Provider value={{ registry, open: () => {} }}><PaneProvider scope={scope}><PersistentPane open={open} change={change} /><RestoreButton /></PaneProvider></PaletteContext.Provider>
  const view = render(ui('session-a', true))
  act(() => registry.getSnapshot().items.find(x => x.id === 'focus-mode')!.run())
  view.rerender(ui('session-b', false))
  if (returnToFirst) view.rerender(ui('session-a', false))
  fireEvent.click(screen.getByText('Restore'))
  expect(change).not.toHaveBeenCalled()
})

it('shares bottom-edge keyboard sizing, reset, collapse and focus', () => {
  render(<Harness side="bottom" />)
  const divider = screen.getByRole('separator')
  expect(divider).toHaveAttribute('aria-orientation', 'horizontal')
  fireEvent.keyDown(divider, { key: 'ArrowUp' })
  expect(divider).toHaveAttribute('aria-valuenow', '320')
  fireEvent.doubleClick(divider)
  expect(divider).toHaveAttribute('aria-valuenow', '300')
  fireEvent.click(screen.getByRole('button', { name: 'Collapse fixture' }))
  expect(screen.getByRole('button', { name: 'Toggle fixture' })).toHaveFocus()
})

it('keeps controlled panel and shell sizes per person without overwriting saved widths on viewport clamps', () => {
  const first = render(<DimensionsHarness max={600} />)
  fireEvent.keyDown(screen.getByRole('separator'), { key: 'ArrowLeft', shiftKey: true })
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '380')
  first.rerender(<DimensionsHarness max={320} />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '320')
  first.rerender(<DimensionsHarness max={600} />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '380')
  account.current = 'person-b'; first.rerender(<DimensionsHarness max={600} />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '300')
  account.current = 'person-a'; first.rerender(<DimensionsHarness max={600} />)
  expect(screen.getByRole('separator')).toHaveAttribute('aria-valuenow', '380')
})
function DimensionsHarness({ max }: { max: number }) {
  const dimensions = usePaneDimensions('controlled', { min: 200, max, default: 300 })
  return <SidePane layout={{ ...dimensions, open: true, toggle: () => {}, close: () => {} }} label="Controlled" toggleLabel="Toggle controlled" side="right" icon="□"><input /></SidePane>
}
