import { render } from '@testing-library/react'
import { expect, it } from 'vitest'
import { page } from 'vitest/browser'
import ShellView from './ShellView'
import { FakeShell } from '../test/shellMock'
import '../index.css'

const parsed = () => new Promise(resolve => setTimeout(resolve, 150))
const screenText = (root: HTMLElement) => root.querySelector('.xterm-rows')?.textContent ?? ''

it.each([390, 3840])('shows the server scrollback after a refresh and never answers its old queries at %i', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const shell = new FakeShell('refresh')
  const { container } = render(<div style={{ position: 'fixed', inset: 0, display: 'flex' }}><ShellView transport={shell} /></div>)
  // What this page showed before (for example #257's page-memory replay)...
  shell.emit('OLD-PAGE-LINE\r\n')
  await parsed()
  // ...is superseded by the server's kept scrollback, which carries terminal
  // queries (DA1, cursor position) that were answered when first displayed.
  shell.emitReplay('$ seq 3\r\n1\r\n2\r\n3\r\n\x1b[c\x1b[6nREPLAYED-TAIL')
  await parsed()
  expect(screenText(container)).toContain('REPLAYED-TAIL')
  expect(screenText(container)).not.toContain('OLD-PAGE-LINE')
  expect(shell.inputs).toEqual([])
  // Live output after the replay is answered as usual.
  shell.emit('\x1b[6n')
  await parsed()
  expect(shell.inputs.length).toBe(1)
  const reply = new TextDecoder().decode(shell.inputs[0])
  expect(reply.charCodeAt(0)).toBe(0x1b)
  expect(reply.slice(1)).toMatch(/^\[\d+;\d+R$/)
})
