import { createRef } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import ShellWorkspace from '../test/ShellWorkspaceHarness'
import type { ShellWorkspaceHandle } from './ShellWorkspace'
import { FakeShell } from '../test/shellMock'
import { sessionTabDragType } from './SessionGroups'
import { sessionTabsKey } from '../hooks/useSessionTabs'
import CommandPaletteProvider from './CommandPalette'
import { shellTheme } from '../lib/shellTheme'
import { HostShellConsentRequired, WebSocketShell, ShellRateGroup } from '../shellTransport'
import { useShellCapability } from '../hooks/useShellCapability'
import '../index.css'
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
const auth = vi.hoisted(() => ({ renewal: async () => true as boolean }))
vi.mock('../authClient', () => ({ ensureFreshToken: () => auth.renewal(), getAccessToken: () => 'fixture-token' }))
afterEach(() => { auth.renewal = async () => true; vi.restoreAllMocks(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })
const luminance = (hex: string) => {
  const c = [1,3,5].map(i => parseInt(hex.slice(i,i+2),16)/255).map(v => v <= .04045 ? v/12.92 : ((v+.055)/1.055)**2.4)
  return c[0]*.2126+c[1]*.7152+c[2]*.0722
}
const contrast = (foreground: string, background: string) => {
  const a = luminance(foreground), b = luminance(background)
  return (Math.max(a,b)+.05)/(Math.min(a,b)+.05)
}
const ansiColors = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white', 'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite'] as const
const empty = async () => []
const mount = (props: Partial<React.ComponentProps<typeof ShellWorkspace>> = {}) => {
  const ref = createRef<ShellWorkspaceHandle>(), shells: FakeShell[] = []
  const create = vi.fn(async () => { const p = new FakeShell(String(shells.length + 1)); shells.push(p); return p })
  const view = render(<MemoryRouter initialEntries={['/sessions/session']}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
    <ShellWorkspace sessionId="session" accountId="owner" ref={ref} restoreShells={empty} createShell={create} {...props}><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
  </div></CommandPaletteProvider></MemoryRouter>)
  return { ref, shells, create, view }
}
const open = async () => { await userEvent.click(screen.getByRole('button', { name: 'New shell' })); await waitFor(() => expect(document.querySelector('.xterm-helper-textarea')).toBeTruthy()) }
function dragShell(label: string, edge: 'right' | 'bottom') {
  const tab = screen.getByRole('tab', { name: label }).parentElement!
  const transfer = new DataTransfer()
  act(() => { tab.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, composed: true, dataTransfer: transfer })) })
  expect(transfer.getData(sessionTabDragType)).toContain('shell:')
  const group = document.querySelector<HTMLElement>('[data-group-id]')!, r = group.getBoundingClientRect()
  const position = { dataTransfer: transfer, clientX: edge === 'right' ? r.right - 4 : r.left + r.width / 2, clientY: edge === 'bottom' ? r.bottom - 4 : r.top + r.height / 2 }
  fireEvent(group, new DragEvent('dragover', { bubbles: true, cancelable: true, ...position }))
  expect(screen.getByTestId('drop-overlay')).toBeTruthy()
  fireEvent(group, new DragEvent('drop', { bubbles: true, cancelable: true, ...position }))
}
it.each([390, 3840])('opens, focuses, backgrounds and closes shell tabs at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const { shells, ref, view } = mount()
  await open()
  if (width === 390) expect(view.container.querySelector('.xterm-helper-textarea')).not.toHaveFocus()
  else await waitFor(() => expect(view.container.querySelector('.xterm-helper-textarea')).toHaveFocus())
  expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  expect(screen.getByRole('tab', { name: 'Shell' }).title).toBe('Shell · sandbox · project-1')
  expect(screen.queryByLabelText('Dock shell')).toBeNull()
  expect(document.querySelectorAll('.shell-tabs, .shell-pane-title')).toHaveLength(0)
  shells[0].emit('project $ printf "Shell tab ready\\n"\r\nShell tab ready\r\nproject $ ')
  const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/shell-tab-${width}.png` })
  await userEvent.click(width === 390 ? screen.getByRole('button', { name: /← Chat/ }) : screen.getByRole('tab', { name: /Chat/ }))
  expect(shells[0].disconnected).toBe(false)
  shells[0].emit('output while hidden\r\n')
  await act(async () => { ref.current?.toggle() })
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toHaveTextContent('output while hidden'))
  expect(shells[0].ended).toBe(false)
  await userEvent.click(screen.getByRole('button', { name: 'Close Shell' }))
  await waitFor(() => expect(shells[0].ended).toBe(true))
  expect(screen.queryByRole('tab', { name: 'Shell' })).toBeNull()
})
it('reorders, drops shells into the split overlay, refits and restores positions on reload', async () => {
  await page.viewport(3840, 2160)
  const first = mount()
  await open(); await open(); await open()
  const terminal = document.querySelector('[data-shell-id="1"] .xterm-helper-textarea')
  const shell3 = screen.getByRole('tab', { name: 'Shell 3' })
  shell3.focus(); await userEvent.keyboard('{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}')
  expect(screen.getAllByRole('tab').map(t => t.textContent?.trim())).toEqual(['Chat', '›_ Shell', '›_ Shell 3', '›_ Shell 2'])
  await userEvent.click(screen.getByRole('tab', { name: 'Shell' }))
  await waitFor(() => expect(first.shells[0].resizes.length).toBeGreaterThan(0))
  const cols = first.shells[0].resizes.at(-1)![0]
  dragShell('Shell', 'right')
  await waitFor(() => expect(document.querySelectorAll('[data-group-id]')).toHaveLength(2))
  await waitFor(() => expect(first.shells[0].resizes.at(-1)![0]).toBeLessThan(cols))
  expect(document.querySelector('[data-shell-id="1"] .xterm-helper-textarea')).toBe(terminal)
  const divider = screen.getByRole('separator', { name: 'Resize tab groups 1' })
  const splitCols = first.shells[0].resizes.at(-1)![0]
  divider.focus(); await userEvent.keyboard('{ArrowLeft}')
  await waitFor(() => expect(first.shells[0].resizes.at(-1)![0]).toBeGreaterThan(splitCols))
  expect(first.shells.every(s => !s.disconnected)).toBe(true)
  const saved = JSON.parse(localStorage.getItem(sessionTabsKey('owner', 'session'))!)
  first.view.unmount()
  const restored = ['1', '2', '3'].map(id => new FakeShell(id))
  mount({ restoreShells: async () => restored })
  await waitFor(() => expect(document.querySelectorAll('[data-shell-id]')).toHaveLength(3))
  expect(JSON.parse(localStorage.getItem(sessionTabsKey('owner', 'session'))!).layout).toEqual(saved.layout)
  expect(screen.getByRole('tab', { name: 'Shell' })).toHaveAttribute('aria-selected', 'true')
  expect(document.querySelectorAll('[data-group-id]')).toHaveLength(2)
})
it.each([390, 3840])('anchors launch errors to the initiating button and closes dead shells at %i px', async width => {
  await page.viewport(width, 900)
  const shell = new FakeShell('dead'); shell.end = vi.fn().mockRejectedValue(new Error('offline'))
  mount({ restoreShells: async () => [shell], createShell: async () => { throw new Error('Shell unavailable.') } })
  await screen.findByRole('button', { name: 'Close Shell' }).catch(async () => { await screen.findByRole('button', { name: /open tab/ }) })
  const button = screen.getByRole('button', { name: 'New shell' })
  await userEvent.click(button)
  const alert = await screen.findByRole('alertdialog')
  const a = alert.getBoundingClientRect(), b = button.getBoundingClientRect()
  expect(a.top).toBeLessThan(b.bottom + 60)
  expect(a.right).toBeLessThanOrEqual(width)
  expect(a.right).toBeGreaterThan(b.left)
  await userEvent.click(within(alert).getByRole('button', { name: 'Dismiss' }))
  if (width === 390) { await userEvent.click(screen.getByRole('button', { name: /open tab/ })); await userEvent.click(screen.getByRole('menuitem', { name: 'Shell' })) }
  else await userEvent.click(screen.getByRole('tab', { name: 'Shell' }))
  shell.reason = 'elsewhere'
  act(() => shell.state?.('disconnected'))
  expect(screen.getByText('Controlled in another tab.')).toBeTruthy()
  expect(screen.getByRole('button', { name: 'Take over' })).toBeTruthy()
  await userEvent.click(screen.getByRole('button', { name: 'Close Shell' }))
  expect(screen.queryByRole('tab', { name: 'Shell' })).toBeNull()
  expect(shell.end).toHaveBeenCalledOnce()
  expect(screen.queryByRole('alertdialog')).toBeNull()
})
it.each([390, 3840])('applies live dark/light tokens and keeps app shortcuts out of terminal input at %i px', async width => {
  await page.viewport(width, 900)
  document.documentElement.dataset.theme = 'dark'
  const { shells } = mount()
  await open()
  const input = document.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')!
  input.focus()
  await waitFor(() => expect(input).toHaveFocus())
  const theme = shellTheme(input)
  expect(theme.fontFamily).toContain('Cascadia Mono')
  expect(theme.theme.background).toBe('#0d1117')
  expect(Object.keys(theme.theme)).toHaveLength(22)
  document.documentElement.dataset.theme = 'light'
  await waitFor(() => expect(getComputedStyle(document.querySelector('.xterm .xterm-scrollable-element')!).backgroundColor).toBe('rgb(238, 241, 246)'))
  document.documentElement.dataset.theme = 'light'
  expect(shellTheme(input).theme.red).toBe('#b83636')
  await userEvent.keyboard('echo hello')
  expect(new TextDecoder().decode(Uint8Array.from(shells[0].inputs.flatMap(b => [...b])))).toBe('echo hello')
  const count = shells[0].inputs.length
  for (const modifier of [/Mac|iPhone|iPad|iPod/.test(navigator.platform) ? 'Meta' : 'Control']) {
    input.focus()
    await userEvent.keyboard(`{${modifier}>}k{/${modifier}}`)
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeTruthy()
    expect(shells[0].inputs).toHaveLength(count)
    await userEvent.keyboard('{Escape}')
  }
  input.focus()
  await userEvent.keyboard('{Alt>}1{/Alt}')
  expect(document.querySelector('[data-shell-id]')!.closest('[hidden]')).toBeNull()
  expect(shells[0].inputs.length).toBeGreaterThan(count)
})
it('explains the four-shell limit in the button and palette', async () => {
  await page.viewport(3840, 900)
  const { create } = mount()
  for (let n = 0; n < 4; n++) await open()
  const button = screen.getByRole('button', { name: 'New shell' })
  expect(button).toHaveAttribute('aria-description', expect.stringContaining('four shells')); expect(button.title).toContain('four shells')
  await userEvent.keyboard('{Control>}k{/Control}')
  fireEvent.change(screen.getByRole('combobox'), { target: { value: '>New shell' } })
  expect(screen.getByRole('option')).toHaveTextContent('four shells')
  expect(create).toHaveBeenCalledTimes(4)
})
it('keeps a fresh real transport connected during initial fit and repeated resize notifications', async () => {
  await page.viewport(3840, 900)
  class Socket {
    static OPEN = 1
    static instance: Socket
    readyState = 1; bufferedAmount = 0
    onopen?: () => void
    onclose?: () => void
    frames: { type: string; cols?: number; rows?: number }[] = []
    constructor() { Socket.instance = this; queueMicrotask(() => this.onopen?.()) }
    send(data: string) { this.frames.push(JSON.parse(data)); if (this.frames.length > 32) this.onclose?.() }
    close() {}
  }
  vi.stubGlobal('WebSocket', Socket)
  const NativeObserver = ResizeObserver
  let notify: (() => void) | undefined
  vi.stubGlobal('ResizeObserver', class extends NativeObserver {
    private callback: ResizeObserverCallback
    constructor(callback: ResizeObserverCallback) { super(callback); this.callback = callback }
    observe(target: Element, options?: ResizeObserverOptions) {
      if (target.matches('.shell-terminal-renderer')) notify = () => this.callback([], this)
      super.observe(target, options)
    }
  })
  const shell = new WebSocketShell({ handle: 'fresh', shell_id: 'fresh', runtime: 'host', folder: 'project' }, new ShellRateGroup())
  mount({ createShell: async () => { await shell.reconnect(); return shell } })
  await open()
  await waitFor(() => expect(Socket.instance.frames.some(f => f.type === 'resize')).toBe(true))
  for (let i = 0; i < 40; i++) await act(async () => { notify?.(); await new Promise(requestAnimationFrame) })
  expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  const resizes = Socket.instance.frames.filter(f => f.type === 'resize')
  expect(resizes.length).toBeLessThanOrEqual(3) // The connecting banner can change the initial row count.
  expect(new Set(resizes.map(f => `${f.cols}:${f.rows}`)).size).toBe(resizes.length)
})
it.each([390, 3840])('keeps consent above a running shell and preserves it when canceled at %i', async width => {
  await page.viewport(width, 900)
  const shell = new FakeShell('retained')
  const challenge = { challenge_id: 'fresh', machine_id: 'machine', account: 'user', uid: 1000, shell: '/bin/bash', cwd: '/project' }
  const { ref } = mount({ restoreShells: async () => [shell], createShell: async () => { throw new HostShellConsentRequired(challenge) } })
  await waitFor(() => expect(shell.output).toBeDefined())
  await act(async () => { ref.current?.toggle() })
  const input = document.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')!
  input.focus()
  await waitFor(() => expect(input).toHaveFocus())
  await userEvent.click(screen.getByRole('button', { name: 'New shell' }))
  const dialog = await screen.findByRole('dialog', { name: 'Allow a shell on this machine?' })
  expect(dialog.closest('[inert]')).toBeNull()
  expect(input.closest('[inert]')).not.toBeNull()
  const cancel = within(dialog).getByRole('button', { name: 'Cancel' })
  expect(cancel).toHaveFocus()
  await userEvent.keyboard('{Shift>}{Tab}{/Shift}')
  expect(within(dialog).getByRole('region', { name: 'Host shell details' })).toHaveFocus()
  await userEvent.keyboard('{Escape}')
  expect(screen.queryByRole('dialog', { name: 'Allow a shell on this machine?' })).toBeNull()
  expect(document.querySelector('.xterm-helper-textarea')).toBe(input)
  expect(input.closest('[inert]')).toBeNull()
  expect(screen.getByRole('button', { name: 'New shell' })).toHaveFocus()
  expect(shell.ended).toBe(false); expect(shell.disconnected).toBe(false)
})
it.each([390, 3840])('anchors a live shell error to its tab while terminal input owns focus at %i', async width => {
  await page.viewport(width, 900)
  const shell = new FakeShell('error')
  let fail: ((message: string) => void) | undefined
  const subscribe = shell.subscribe.bind(shell)
  shell.subscribe = (output, state, error) => { fail = error; return subscribe(output, state) }
  mount({ createShell: async () => shell })
  await open()
  ;(document.querySelector('.xterm-helper-textarea') as HTMLElement).focus()
  await waitFor(() => expect(document.querySelector('.xterm-helper-textarea')).toHaveFocus())
  const tab = screen.getByRole('tab', { name: 'Shell' }).getBoundingClientRect()
  act(() => fail?.('The shell helper stopped.'))
  const alert = await screen.findByRole('alertdialog', { name: 'Shell stopped' })
  const box = alert.getBoundingClientRect()
  expect(box.top).toBeGreaterThanOrEqual(tab.bottom)
  expect(box.top).toBeLessThan(tab.bottom + 60)
  expect(box.right).toBeLessThanOrEqual(width)
  await userEvent.click(within(alert).getByRole('button', { name: 'Dismiss' }))
  expect(screen.getByRole('tab', { name: 'Shell' })).toHaveFocus()
})
it('retains all three owned transports across capability refresh instead of replacing them with attached handles', async () => {
  await page.viewport(3840, 900)
  const shells = [new FakeShell('one'), new FakeShell('two'), new FakeShell('three')]
  const restore = vi.fn().mockResolvedValueOnce([shells[0]]).mockResolvedValue(shells.map(shell => new WebSocketShell({ ...shell.info, attached: true }, new ShellRateGroup())))
  let next = 1
  const create = async () => shells[next++]
  const ref = createRef<ShellWorkspaceHandle>()
  const ui = (enabled: boolean, capabilityPending: boolean) => <div style={{ height: '100vh' }}><ShellWorkspace sessionId="refresh" accountId="owner" ref={ref} enabled={enabled} capabilityPending={capabilityPending} restoreShells={restore} createShell={create}>Chat</ShellWorkspace></div>
  const view = render(ui(true, false))
  await waitFor(() => expect(shells[0].output).toBeDefined())
  await act(async () => { ref.current?.open() })
  await act(async () => { ref.current?.open() })
  await waitFor(() => expect(document.querySelectorAll('.xterm-helper-textarea')).toHaveLength(3))
  const terminals = [...document.querySelectorAll('.xterm-helper-textarea')]
  await act(async () => { view.rerender(ui(false, true)) })
  await act(async () => { view.rerender(ui(true, false)) })
  await waitFor(() => expect(restore).toHaveBeenCalledTimes(2))
  expect([...document.querySelectorAll('.xterm-helper-textarea')]).toEqual(terminals)
  expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  expect(shells.every(shell => !shell.disconnected)).toBe(true)
  shells[2].emit('still owned\r\n')
  await waitFor(() => expect(document.querySelector('[data-shell-id="three"] .xterm-screen')).toHaveTextContent('still owned'))
})

it.each(['MacIntel', 'Linux x86_64'])('H3/M2: terminal keys never close or switch tabs on %s', async platform => {
  await page.viewport(1200, 900)
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
  const { shells } = mount()
  await open()
  const input = document.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')!
  input.focus()
  const controls = platform === 'MacIntel' ? ['w', '[', ']', '1', 'k', 'p'] : ['w', '[', ']', '1']
  for (const key of controls) {
    const before = shells[0].inputs.length
    await userEvent.keyboard(`{Control>}${key === '[' ? '[[' : key}{/Control}`)
    expect(shells[0].ended, key).toBe(false)
    expect(screen.getByRole('tab', { name: 'Shell' }), key).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByRole('dialog', { name: 'Command palette' }), key).toBeNull()
    if (['w','[','k','p'].includes(key)) expect(shells[0].inputs.length, key).toBeGreaterThan(before)
  }
  for (const key of ['k','p']) {
    input.focus()
    const count = shells[0].inputs.length
    await userEvent.keyboard(`{${platform === 'MacIntel' ? 'Meta' : 'Control'}>}${key}{/${platform === 'MacIntel' ? 'Meta' : 'Control'}}`)
    expect(await screen.findByRole('dialog', { name: key === 'k' ? 'Command palette' : 'Quick open' })).toBeTruthy()
    expect(shells[0].inputs).toHaveLength(count)
    await userEvent.keyboard('{Escape}')
  }
})
it.each([390, 3840])('L1: restore never steals focus and phone selection never focuses input at %i', async width => {
  await page.viewport(width, 900)
  const first = mount(); await open(); first.view.unmount()
  const outside = document.createElement('input'); document.body.append(outside); outside.focus()
  try {
    const shell = new FakeShell('1')
    const next = mount({ restoreShells: async () => [shell] })
    await waitFor(() => expect(shell.output).toBeDefined())
    await new Promise(requestAnimationFrame)
    expect(outside).toHaveFocus()
    await act(async () => { next.ref.current?.toggle() })
    const input = document.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')!
    if (width === 390) expect(input).not.toHaveFocus()
    else await waitFor(() => expect(input).toHaveFocus())
  } finally { outside.remove() }
})
it.each(['dark', 'light'])('M3: ANSI pairs retain meaning and contrast in %s mode', async mode => {
  document.documentElement.dataset.theme = mode
  const { theme: colors } = shellTheme(document.body)
  expect(luminance(colors.black!)).toBeLessThan(.08)
  expect(luminance(colors.white!)).toBeGreaterThan(.5)
  const basePairs = [['black','white'], ['white','black'], ['brightWhite','brightBlack'], ['brightBlack','brightWhite']] as const
  // Status backgrounds use dark ink in dark mode and light paper in light mode.
  // Black-on-green/cyan is deliberately not required for dark light-theme greens.
  const statusInk = mode === 'light' ? 'brightWhite' : 'black'
  const statusBackgrounds = ['green', 'cyan', 'brightGreen', 'brightYellow', 'brightBlue'] as const
  for (const [foreground, background] of [...basePairs, ...statusBackgrounds.map(background => [statusInk, background] as const)]) {
    expect(contrast(colors[foreground]!, colors[background]!), `${mode} ${foreground}/${background}`).toBeGreaterThanOrEqual(4.5)
  }
})
it.each(['k', 'p'])('M2: macOS Ctrl+%s reaches the shell', async key => {
  await page.viewport(1200, 900)
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue('MacIntel')
  const { shells } = mount(); await open()
  const input = document.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea')!
  input.focus()
  await userEvent.keyboard(`{Control>}${key}{/Control}`)
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(new TextDecoder().decode(Uint8Array.from(shells[0].inputs.flatMap(bytes => [...bytes])))).toBe(key === 'k' ? '\x0b' : '\x10')
})
it.each([390, 3840])('L3: keyboard and touch can reveal the anchored limit reason at %i', async width => {
  await page.viewport(width, 900)
  const { create } = mount()
  for (let n = 0; n < 4; n++) await open()
  const button = screen.getByRole('button', { name: 'New shell' })
  // Firefox can retain an activeElement in an unfocused test iframe. Native
  // Enter then delivers key events but does not activate the button.
  window.focus()
  button.focus()
  await waitFor(() => expect(document.hasFocus()).toBe(true))
  expect(button).toHaveFocus()
  await userEvent.keyboard('{Enter}')
  const dialog = await screen.findByRole('alertdialog')
  expect(dialog).toHaveTextContent('four shells')
  expect(dialog.getBoundingClientRect().top).toBeLessThan(button.getBoundingClientRect().bottom + 60)
  await userEvent.click(within(dialog).getByRole('button', { name: 'Dismiss' }))
  // This click exercises the same activation path as touch, without hover/tooltips.
  await userEvent.click(button)
  expect(await screen.findByRole('alertdialog')).toHaveTextContent('four shells')
  expect(create).toHaveBeenCalledTimes(4)
})

it.each(['dark', 'light', 'system'])('N1: ANSI ink stays readable against the %s theme background', setting => {
  if (setting === 'system') document.documentElement.removeAttribute('data-theme')
  else document.documentElement.dataset.theme = setting
  const mode = setting === 'system' ? matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark' : setting
  const { theme: colors } = shellTheme(document.body)
  // Light white/brightWhite are paper colors, paired with ink in the M3 guard.
  // Dark black/brightBlack are ink colors, paired with paper in the same guard.
  const paperOrInk = mode === 'light' ? ['white', 'brightWhite'] : ['black', 'brightBlack']
  for (const name of ansiColors) if (!paperOrInk.includes(name)) {
    expect.soft(contrast(colors[name]!, colors.background!), `${setting} ${name}`).toBeGreaterThanOrEqual(4.5)
  }
})

it.each(['Linux x86_64', 'MacIntel'])('reserves tab focus inside terminals on %s and retains hidden shell connections', async platform => {
  vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
  await page.viewport(1800, 1100)
  const { shells } = mount()
  await open(); dragShell('Shell', 'right')
  const input = document.querySelector<HTMLTextAreaElement>('[data-shell-id="1"] .xterm-helper-textarea')!
  const modifier = platform === 'MacIntel' ? 'Meta' : 'Control'
  input.focus()
  await userEvent.keyboard(`{${modifier}>}{Shift>}{Enter}{/Shift}{/${modifier}}`)
  expect(screen.getByRole('button', { name: 'Exit tab focus' })).toBeVisible()
  expect(input).toHaveFocus()
  expect(shells[0].inputs).toHaveLength(0)
  await userEvent.keyboard(`{${modifier}>}{Shift>}{Enter}{/Shift}{/${modifier}}`)
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  await userEvent.dblClick(screen.getByRole('tab', { name: /Chat/ }))
  expect(input.closest('.session-group')).not.toBeVisible()
  expect(input.isConnected).toBe(true)
  shells[0].emit('retained output while another tab is focused\r\n')
  expect(shells[0].disconnected).toBe(false)
  await userEvent.click(screen.getByRole('button', { name: 'Exit tab focus' }))
  expect(input.isConnected).toBe(true)
  expect(input.closest('.session-group')).toBeVisible()
  await waitFor(() => expect(document.querySelector('[data-shell-id="1"] .xterm-screen')).toHaveTextContent('retained output'))
  expect(shells[0].ended).toBe(false)
})

// Card #257: leaving a session unmounts its workspace; coming back must find the same
// shell running, reattach it and show the scrollback it displayed, at phone and 4K widths.
it.each([390, 3840])('keeps a shell across leaving and returning to its session at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  class Socket {
    static OPEN = 1
    static instances: Socket[] = []
    readyState = 1; bufferedAmount = 0; binaryType = ''
    closed = false
    onopen?: () => void
    onmessage?: (event: { data: ArrayBuffer | string }) => void
    onclose?: () => void
    constructor() { Socket.instances.push(this); queueMicrotask(() => this.onopen?.()) }
    send() {}
    close() { this.closed = true }
  }
  vi.stubGlobal('WebSocket', Socket)
  const session = `return-${width}`
  const listed = { handle: `handle-${width}`, shell_id: `shell-${width}`, runtime: 'host', folder: 'project', attached: false }
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    if (url === `/api/sessions/${session}/shells`) return new Response(JSON.stringify([listed]))
    return new Response('{}', { status: 404 })
  })
  const ui = () => <MemoryRouter initialEntries={[`/sessions/${session}`]}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
    <ShellWorkspace sessionId={session} accountId="owner"><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
  </div></CommandPaletteProvider></MemoryRouter>
  // Phones list background tabs in the kept-open menu; wider screens show the tab strip.
  const show = async () => {
    if (width === 390) { fireEvent.click(await screen.findByRole('button', { name: '1 open tab' })); fireEvent.click(await screen.findByRole('menuitem', { name: /Shell/ })) }
    else fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  }
  const first = render(ui())
  await waitFor(() => expect(Socket.instances).toHaveLength(1))
  await show()
  const bytes = (text: string) => new Uint8Array([...text].map(c => c.charCodeAt(0))).buffer
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toBeTruthy())
  act(() => { Socket.instances[0].onmessage?.({ data: bytes('project $ echo kept\r\nkept\r\nproject $ ') }) })
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toHaveTextContent('kept'))
  // Leave: the session view unmounts. Its socket closes; nothing ends the shell.
  first.unmount()
  expect(Socket.instances[0].closed).toBe(true)
  await new Promise(resolve => setTimeout(resolve, 50))
  expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
  // Return: the same shell tab comes back, reattaches and shows what it displayed.
  render(ui())
  await waitFor(() => expect(Socket.instances).toHaveLength(2))
  await show()
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toHaveTextContent('echo kept'))
  expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
})

// #257 review: return while the token renewal is pending, leave, and return again. The
// abandoned view and the current one share the kept transport and the renewal; the
// abandoned one must not disconnect the view the person is looking at.
it.each([390, 3840])('stays connected after a rapid leave and return during token renewal at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  class Socket {
    static OPEN = 1
    static instances: Socket[] = []
    readyState = 1; bufferedAmount = 0; binaryType = ''
    closed = false
    onopen?: () => void
    onmessage?: (event: { data: ArrayBuffer | string }) => void
    onclose?: () => void
    constructor() { Socket.instances.push(this); queueMicrotask(() => this.onopen?.()) }
    send() {}
    close() { this.closed = true }
  }
  vi.stubGlobal('WebSocket', Socket)
  const session = `rapid-${width}`
  const listed = { handle: `rapid-handle-${width}`, shell_id: `rapid-shell-${width}`, runtime: 'host', folder: 'project', attached: false }
  const inventories = () => apiFetch.mock.calls.filter(([url]) => url === `/api/sessions/${session}/shells`).length
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    if (url === `/api/sessions/${session}/shells`) return new Response(JSON.stringify([listed]))
    return new Response('{}', { status: 404 })
  })
  const ui = () => <MemoryRouter initialEntries={[`/sessions/${session}`]}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
    <ShellWorkspace sessionId={session} accountId="owner"><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
  </div></CommandPaletteProvider></MemoryRouter>
  const show = async () => {
    if (width === 390) { fireEvent.click(await screen.findByRole('button', { name: '1 open tab' })); fireEvent.click(await screen.findByRole('menuitem', { name: /Shell/ })) }
    else fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  }
  const bytes = (text: string) => new Uint8Array([...text].map(c => c.charCodeAt(0))).buffer
  const first = render(ui())
  await waitFor(() => expect(Socket.instances).toHaveLength(1))
  await show()
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toBeTruthy())
  act(() => { Socket.instances[0].onmessage?.({ data: bytes('project $ echo rapid\r\nrapid\r\nproject $ ') }) })
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toHaveTextContent('echo rapid'))
  first.unmount()
  // One shared renewal, as authClient coalesces concurrent renewSilently calls.
  let renew!: (ok: boolean) => void
  const renewal = new Promise<boolean>(resolve => { renew = resolve })
  auth.renewal = () => renewal
  const abandoned = render(ui())
  await waitFor(() => expect(inventories()).toBe(2))
  await new Promise(resolve => setTimeout(resolve, 20))
  abandoned.unmount()
  render(ui())
  await waitFor(() => expect(inventories()).toBe(3))
  await new Promise(resolve => setTimeout(resolve, 20))
  await act(async () => { renew(true); await renewal })
  await waitFor(() => expect(Socket.instances).toHaveLength(2))
  await show()
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toHaveTextContent('echo rapid'))
  await new Promise(resolve => setTimeout(resolve, 50))
  expect(Socket.instances[1].closed).toBe(false)
  expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
})

class LiveSocket {
  static OPEN = 1
  static instances: LiveSocket[] = []
  readyState = 1; bufferedAmount = 0; binaryType = ''
  closed = false
  onopen?: () => void
  onmessage?: (event: { data: ArrayBuffer | string }) => void
  onclose?: () => void
  constructor() { LiveSocket.instances.push(this); queueMicrotask(() => this.onopen?.()) }
  send() {}
  close() { this.closed = true }
}
const pending = <T,>() => { let resolve!: (value: T) => void; const promise = new Promise<T>(r => { resolve = r }); return { promise, resolve } }
// The production relationship: SessionDetail passes enabled and capabilityPending from useShellCapability.
function CapabilityWorkspace({ session }: { session: string }) {
  const capability = useShellCapability(session, 'running')
  return <MemoryRouter initialEntries={[`/sessions/${session}`]}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
    <ShellWorkspace sessionId={session} accountId="owner" enabled={capability === true} capabilityPending={capability === null}><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
  </div></CommandPaletteProvider></MemoryRouter>
}
// #257 round 2: return with the token renewal pending, then a window focus refreshes the
// shell capability. The same mounted view restores twice with one rate group and one
// shared renewal; the abandoned restore must not close the newer restore's socket.
it.each([390, 3840])('keeps the newer restore connected when a capability refresh restarts it during renewal at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  LiveSocket.instances = []
  vi.stubGlobal('WebSocket', LiveSocket)
  const session = `focus-${width}`
  const listed = { handle: `focus-handle-${width}`, shell_id: `focus-shell-${width}`, runtime: 'host', folder: 'project', attached: false }
  const inventories = () => apiFetch.mock.calls.filter(([url, init]) => url === `/api/sessions/${session}/shells` && !init?.method).length
  const capability: { reply?: Promise<Response> } = {}
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    if (url === `/api/sessions/${session}/shell-capability`) return capability.reply ? await capability.reply : new Response(JSON.stringify({ available: true }))
    if (url === `/api/sessions/${session}/shells`) return new Response(JSON.stringify([listed]))
    return new Response('{}', { status: 404 })
  })
  const show = async () => {
    if (width === 390) { fireEvent.click(await screen.findByRole('button', { name: '1 open tab' })); fireEvent.click(await screen.findByRole('menuitem', { name: /Shell/ })) }
    else fireEvent.click(await screen.findByRole('tab', { name: 'Shell' }))
  }
  const first = render(<CapabilityWorkspace session={session} />)
  await waitFor(() => expect(LiveSocket.instances).toHaveLength(1))
  await show()
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toBeTruthy())
  act(() => { LiveSocket.instances[0].onmessage?.({ data: new TextEncoder().encode('project $ echo focus\r\nfocus\r\n').buffer }) })
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toHaveTextContent('echo focus'))
  first.unmount()
  // Return while the renewal is pending.
  const renewal = pending<boolean>()
  auth.renewal = () => renewal.promise
  render(<CapabilityWorkspace session={session} />)
  await waitFor(() => expect(inventories()).toBe(2))
  await new Promise(resolve => setTimeout(resolve, 20))
  // Focus refreshes capability: pending (the restore effect is abandoned), then available again.
  const reply = pending<Response>()
  capability.reply = reply.promise
  await act(async () => { window.dispatchEvent(new Event('focus')) })
  await new Promise(resolve => setTimeout(resolve, 20))
  await act(async () => { reply.resolve(new Response(JSON.stringify({ available: true }))) })
  await waitFor(() => expect(inventories()).toBeGreaterThanOrEqual(3))
  await new Promise(resolve => setTimeout(resolve, 20))
  await act(async () => { renewal.resolve(true); await renewal.promise })
  await waitFor(() => expect(LiveSocket.instances).toHaveLength(2))
  await new Promise(resolve => setTimeout(resolve, 50))
  expect(LiveSocket.instances[1].closed).toBe(false)
  await show()
  await waitFor(() => expect(document.querySelector('.xterm-screen')).toHaveTextContent('echo focus'))
  expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
})
// #257 round 2: the person opens a shell and leaves while its token renewal is pending.
// The returning view adopts the same kept transport and connects it; when the old open
// finally completes in the view that went away, it must not close the returning view's socket.
it.each([390, 3840])('an abandoned open never disconnects the shell the returning view adopted at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  LiveSocket.instances = []
  vi.stubGlobal('WebSocket', LiveSocket)
  const session = `open-${width}`
  const info = { handle: `open-handle-${width}`, shell_id: `open-shell-${width}`, runtime: 'host', folder: 'project' }
  let listed: object[] = []
  apiFetch.mockImplementation(async (url: string, init?: RequestInit) => {
    if (init?.method === 'DELETE') return new Response(null, { status: 204 })
    if (url === `/api/sessions/${session}/shells` && init?.method === 'POST') { listed = [{ ...info, attached: false }]; return new Response(JSON.stringify(info)) }
    if (url === `/api/sessions/${session}/shells`) return new Response(JSON.stringify(listed))
    return new Response('{}', { status: 404 })
  })
  const ui = () => <MemoryRouter initialEntries={[`/sessions/${session}`]}><CommandPaletteProvider><div style={{ height: '100dvh' }}>
    <ShellWorkspace sessionId={session} accountId="owner"><div className="agent-composer"><textarea aria-label="Composer" /></div></ShellWorkspace>
  </div></CommandPaletteProvider></MemoryRouter>
  const first = render(ui())
  await waitFor(() => expect(document.querySelector('[data-shells-restored="true"]')).toBeTruthy())
  const slow = pending<boolean>()
  let renewals = 0
  auth.renewal = () => renewals++ === 0 ? slow.promise : Promise.resolve(true)
  fireEvent.click(screen.getByRole('button', { name: 'New shell' }))
  await waitFor(() => expect(renewals).toBe(1))
  // Leave while the open waits for its renewal, and return.
  first.unmount()
  render(ui())
  await waitFor(() => expect(LiveSocket.instances).toHaveLength(1))
  await new Promise(resolve => setTimeout(resolve, 20))
  // The old open finishes last, in the view that went away.
  await act(async () => { slow.resolve(true); await slow.promise })
  await new Promise(resolve => setTimeout(resolve, 50))
  expect(LiveSocket.instances).toHaveLength(1)
  expect(LiveSocket.instances[0].closed).toBe(false)
  expect(screen.queryByText(/^(Disconnected\.|Controlled in another tab\.)$/)).toBeNull()
  expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
})
