import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useMemo } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import CommandPaletteProvider, { PaletteButton } from './CommandPalette'
import { usePaletteCommands } from '../commands/context'
import type { PaletteProvider } from '../commands/registry'
import '../index.css'
import './IconButton.css'

function Feature({ run, providers }: { run: () => void; providers: PaletteProvider[] }) {
  const commands = useMemo(() => Array.from({ length: 80 }, (_, i) => ({
    id: `command-${i}`, title: `Command ${String(i).padStart(2, '0')}: open a long repository path with useful details`, shortcut: i === 0 ? 'Ctrl/⌘+P' : undefined, run,
  })), [run])
  usePaletteCommands(commands, providers)
  return <><PaletteButton /><textarea aria-label="Session composer" /></>
}

for (const width of [390, 3840]) describe(`command palette at ${width}px`, () => {
  it('uses the viewport, scrolls the active option into view, traps focus, and restores it on Esc', async () => {
    await page.viewport(width, 900)
    const run = vi.fn()
    render(<MemoryRouter initialEntries={['/sessions/s']}><CommandPaletteProvider><Feature run={run} providers={[]} /></CommandPaletteProvider></MemoryRouter>)
    const trigger = screen.getByRole('button', { name: 'Search commands and files' })
    trigger.focus()
    await userEvent.keyboard('{Control>}k{/Control}')
    const input = screen.getByRole('combobox'), dialog = screen.getByRole('dialog')
    expect(document.activeElement).toBe(input)
    const box = dialog.getBoundingClientRect()
    expect(box.width).toBeGreaterThan(width === 390 ? 380 : 1900)
    expect(box.right).toBeLessThanOrEqual(width)
    expect(box.bottom).toBeLessThanOrEqual(900)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    expect(screen.getAllByRole('option')).toHaveLength(80)
    await userEvent.keyboard('{ArrowUp}')
    await waitFor(() => expect(screen.getByRole('listbox').scrollTop).toBeGreaterThan(0))
    const option = document.getElementById(input.getAttribute('aria-activedescendant')!)!
    const listBox = screen.getByRole('listbox').getBoundingClientRect()
    expect(option.getBoundingClientRect().bottom).toBeLessThanOrEqual(listBox.bottom + 1)
    await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close search' }))
    await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
    expect(document.activeElement).toBe(input)
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(trigger)
    await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    await userEvent.keyboard('{Tab}{Enter}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(run).not.toHaveBeenCalled()
  })
  it('offers the phone search button and quick open with a fake multirepository provider', async () => {
    await page.viewport(width, 900)
    const opened = vi.fn()
    const providers: PaletteProvider[] = [{ id: 'files', mode: 'files', search: async () => ({ items: [
      { id: 'app', title: 'app/src/HelloWorld.tsx', detail: 'App', run: () => opened('app/src/HelloWorld.tsx') },
      { id: 'lib', title: 'lib/src/HelloWorld.tsx', detail: 'Library', run: () => opened('lib/src/HelloWorld.tsx') },
    ], note: 'Partial results: the workspace search limit was reached.' }) }]
    render(<MemoryRouter initialEntries={['/sessions/s']}><CommandPaletteProvider><Feature run={vi.fn()} providers={providers} /></CommandPaletteProvider></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    expect(screen.getByRole('dialog').textContent).toContain('Command palette')
    await userEvent.keyboard('{Escape}')
    await userEvent.keyboard('{Meta>}p{/Meta}')
    let input = screen.getByRole('combobox', { name: 'Search session files' })
    fireEvent.change(input, { target: { value: 'hlw' } })
    await waitFor(() => expect(screen.getAllByRole('option')).toHaveLength(2))
    expect(screen.getByRole('status').textContent).toContain('Partial results')
    await userEvent.keyboard('{ArrowDown}{Enter}')
    expect(opened).toHaveBeenCalledWith('lib/src/HelloWorld.tsx')
    await userEvent.keyboard('{Control>}p{/Control}')
    input = screen.getByRole('combobox')
    fireEvent.change(input, { target: { value: '>Command 07' } })
    expect(screen.getAllByRole('option')).toHaveLength(1)
    await act(async () => { fireEvent.keyDown(input, { key: 'Escape' }) })
  })
})
