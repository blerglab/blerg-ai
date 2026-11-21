import { act, render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { CommandRegistry } from '../commands/registry'
import KeyboardShortcuts from './KeyboardShortcuts'
import { platformShortcut } from '../commands/shortcuts'

it('formats shared bindings for each platform without changing platform-specific bindings', () => {
  expect(platformShortcut('Ctrl/⌘+K', 'MacIntel')).toBe('⌘+K')
  expect(platformShortcut('Ctrl / ⌘ `', 'Linux')).toBe('Ctrl `')
  expect(platformShortcut('Ctrl/⌘+Shift+Enter', 'Win32')).toBe('Ctrl+Shift+Enter')
  expect(platformShortcut('Control+1', 'MacIntel')).toBe('Control+1')
  expect(platformShortcut('Alt+]', 'Linux')).toBe('Alt+]')
})
it('follows live declarations, updates, and feature removal without a parallel shortcut list', () => {
  const registry = new CommandRegistry()
  const dispose = registry.register([{id:'dynamic', title:'First action', shortcut:'Ctrl/⌘+F', shortcutGroup:'Explorer',run(){}}])
  render(<KeyboardShortcuts registry={registry} close={() => {}}/>)
  expect(screen.getByText('First action')).toBeTruthy()
  act(() => dispose.update([{id:'dynamic', title:'Renamed action', shortcut:'Alt+R', shortcutGroup:'Shell',run(){}}]))
  expect(screen.queryByText('First action')).toBeNull()
  expect(screen.getByText('Renamed action')).toBeTruthy()
  expect(screen.getByText('Alt+R')).toBeTruthy()
  expect(screen.getByRole('heading',{name:'Shell'})).toBeTruthy()
  expect(screen.queryByRole('heading',{name:'Explorer'})).toBeNull()
  act(() => dispose())
  expect(screen.queryByText('Renamed action')).toBeNull()
  expect(screen.getByRole('status').textContent).toBe('No matching shortcuts.')
})
