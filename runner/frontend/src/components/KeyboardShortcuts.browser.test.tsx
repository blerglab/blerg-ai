import { useMemo } from 'react'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import CommandPaletteProvider, { PaletteButton } from './CommandPalette'
import { usePaletteCommands } from '../commands/context'
import '../index.css'
import './IconButton.css'

// The palette imports the live client; this fixture does not exercise authentication.
vi.mock('../ws', () => import('../test/wsMock'))

function Feature() {
  usePaletteCommands(useMemo(() => [
    {id:'fixture-session', title:'Next tab', destructive:false, shortcut:'Alt+]', run(){}},
    {id:'fixture-shell', title:'Focus shell', destructive:false, shortcut:'Ctrl/⌘+`', shortcutGroup:'Shell' as const, run(){}},
    {id:'fixture-explorer', title:'Quick open files', destructive:false, shortcut:'Ctrl/⌘+P', shortcutGroup:'Explorer' as const, run(){}},
  ], []))
  return <><PaletteButton/><div className="agent-composer"><textarea aria-label="Composer"/></div><input aria-label="Text field"/><div contentEditable suppressContentEditableWarning role="textbox" aria-label="Editor"/><div className="xterm"><textarea aria-label="Terminal"/></div></>
}
for (const width of [390, 3840]) describe(`keyboard shortcuts ${width}px`, () => {
  it('teaches the palette hotkey and opens searchable registry help from the palette', async () => {
    await page.viewport(width, 900)
    render(<MemoryRouter><CommandPaletteProvider><Feature/></CommandPaletteProvider></MemoryRouter>)
    await userEvent.click(screen.getByRole('button', {name:'Search commands and files'}))
    expect(screen.getByRole('combobox').getAttribute('placeholder')).toMatch(/Ctrl K|⌘K/)
    expect(screen.getByText(/\? all shortcuts/)).toBeTruthy()
    const shotsDir = import.meta.env.VITE_CHROME_SHOTS_DIR
    if (shotsDir) await page.screenshot({path:`${shotsDir}/palette-hotkey-${width}.png`,element:screen.getByRole('dialog')})
    fireEvent.change(screen.getByRole('combobox'), {target:{value:'Keyboard shortcuts'}})
    await userEvent.keyboard('{Enter}')
    const dialog = screen.getByRole('dialog', {name:'Keyboard shortcuts'})
    fireEvent.keyDown(screen.getByRole('searchbox'), {key:'k',ctrlKey:true})
    expect(screen.getAllByRole('dialog')).toHaveLength(1)
    const box = dialog.getBoundingClientRect()
    expect(box.right).toBeLessThanOrEqual(width)
    expect(box.bottom).toBeLessThanOrEqual(900)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    for (const title of ['Everywhere', 'In a session', 'Panes', 'Shell', 'Explorer']) expect(within(dialog).getByRole('heading',{name:title})).toBeTruthy()
    const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
    if (shots) await page.screenshot({path:`${shots}/shortcuts-${width}.png`, element:dialog})
    const keys = [...dialog.querySelectorAll('kbd')]
    expect(keys.length).toBeGreaterThan(0)
    expect(getComputedStyle(keys[0]).display === 'none').toBe(width === 390)
    expect(dialog.textContent).not.toContain('Ctrl/⌘')
    fireEvent.change(within(dialog).getByRole('searchbox'), {target:{value:'Focus shell'}})
    expect(within(dialog).getByText('Focus shell')).toBeTruthy()
    expect(within(dialog).queryByText('Next tab')).toBeNull()
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('button',{name:'Search commands and files'}))
  })
  it('opens on ? outside typing surfaces and leaves composer, fields, editors and terminals alone', async () => {
    await page.viewport(width, 900)
    render(<MemoryRouter><CommandPaletteProvider><Feature/></CommandPaletteProvider></MemoryRouter>)
    for (const label of ['Composer','Text field','Editor','Terminal']) {
      const field = screen.getByLabelText(label)
      field.focus()
      fireEvent.keyDown(field, {key:'?', shiftKey:true})
      expect(screen.queryByRole('dialog')).toBeNull()
    }
    screen.getByRole('button',{name:'Search commands and files'}).focus()
    fireEvent.keyDown(window, {key:'?',shiftKey:true})
    expect(screen.getByRole('dialog',{name:'Keyboard shortcuts'})).toBeTruthy()
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('searchbox')))
    await userEvent.keyboard('{Tab}')
    expect(document.activeElement).toBe(screen.getByRole('button',{name:'Close shortcuts'}))
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    await userEvent.click(screen.getByRole('button', {name:'Search commands and files'}))
    const closeSearch = screen.getByRole('button', {name:'Close search'})
    closeSearch.focus()
    fireEvent.keyDown(closeSearch, {key:'?',shiftKey:true})
    expect(screen.getAllByRole('dialog')).toHaveLength(1)
    expect(screen.getByRole('dialog',{name:'Keyboard shortcuts'})).toBeTruthy()
  })
})
