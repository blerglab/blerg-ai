import { useContext } from 'react'
import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import PaneProvider from './PaneProvider'
import { PaneContext } from '../panes/context'
import { PaletteContext } from '../commands/context'
import { CommandRegistry } from '../commands/registry'

afterEach(() => vi.restoreAllMocks())
function Status() { return <output data-testid="focused">{String(useContext(PaneContext)!.focused)}</output> }

it.each(['Linux x86_64', 'MacIntel'].flatMap(platform => ['modal', 'dialog', 'native-dialog', 'review', 'diff-review'].map(surface => ({ platform, surface }))))('leaves $surface shortcuts with their owners on $platform', ({ platform, surface }) => {
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
  const key = { key: 'Enter', ctrlKey: platform !== 'MacIntel', metaKey: platform === 'MacIntel', shiftKey: true }
  const save = vi.fn()
  const form = <form className={surface === 'diff-review' ? 'review-form review-inline-composer' : 'review-form'} onKeyDown={event => { save(event.defaultPrevented); event.preventDefault() }}><textarea aria-label="Request" /></form>
  const view = render(<PaneProvider scope="/sessions/one"><Status />{
    surface === 'modal' ? <div role="dialog" aria-modal="true" onKeyDown={() => save(false)}><input aria-label="Request" /></div> :
    surface === 'dialog' ? <div role="dialog" onKeyDown={() => save(false)}><input aria-label="Request" /></div> :
    surface === 'native-dialog' ? <dialog open onKeyDown={() => save(false)}><input aria-label="Request" /></dialog> : form
  }</PaneProvider>)
  const input = screen.getByRole('textbox', { name: 'Request' })
  input.focus(); fireEvent.keyDown(input, key)
  expect(save, surface).toHaveBeenCalledWith(false)
  expect(screen.getByTestId('focused'), surface).toHaveTextContent('false')
  view.unmount()
})

it.each(['Linux x86_64', 'MacIntel'])('still reserves focus mode inside a terminal and normal session content on %s', platform => {
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
  const key = { key: 'Enter', ctrlKey: platform !== 'MacIntel', metaKey: platform === 'MacIntel', shiftKey: true }
  const terminal = vi.fn()
  render(<PaneProvider scope="/sessions/one"><Status /><div className="shell-terminal"><div className="xterm"><textarea aria-label="Terminal" onKeyDown={terminal} /></div></div><button>Normal session</button><form className="review-form"><textarea aria-label="Unfocused request" /></form></PaneProvider>)
  const input = screen.getByRole('textbox', { name: 'Terminal' })
  input.focus()
  fireEvent.keyDown(input, key)
  expect(terminal).not.toHaveBeenCalled()
  expect(screen.getByTestId('focused')).toHaveTextContent('true')
  const button = screen.getByRole('button')
  button.focus(); fireEvent.keyDown(button, key)
  expect(screen.getByTestId('focused')).toHaveTextContent('false')
})

it('announces pane focus without mentioning tabs when the command runs outside a session', () => {
  const registry = new CommandRegistry()
  render(<PaletteContext.Provider value={{ registry, open() {} }}><PaneProvider scope="/settings"><Status /></PaneProvider></PaletteContext.Provider>)
  const run = () => registry.getSnapshot().items.find(item => item.id === 'focus-mode')!.run()
  act(run)
  expect(screen.getByTestId('tab-focus-announce')).toHaveTextContent(/^Entered focus mode$/)
  act(run)
  expect(screen.getByTestId('tab-focus-announce')).toHaveTextContent(/^Exited focus mode$/)
})
