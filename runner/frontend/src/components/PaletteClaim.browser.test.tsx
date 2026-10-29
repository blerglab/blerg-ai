import { render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { page, userEvent } from 'vitest/browser'
import { useTranscriptStore } from '@blerglab/chat'
import CommandPaletteProvider, { PaletteButton } from './CommandPalette'
import RunnerCommands from './RunnerCommands'
import SessionDetail from './SessionDetail'
import { Activity, useState } from 'react'
import CommentEditor from '../../../../packages/chat/src/components/review/CommentEditor'
import { sessionTabsKey, viewId } from '../hooks/useSessionTabs'
import { invalidateChatTransport } from '../lib/chatTransport'
import { HostShellConsentRequired, openShell } from '../shellTransport'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import * as wsMock from '../test/wsMock'
import { FakeShell } from '../test/shellMock'
import { apiFetch } from '../apiFetch'
import '../index.css'
import '@blerglab/chat/blerg.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn() }))
vi.mock('../lib/boardLink', async original => ({ ...await original<typeof import('../lib/boardLink')>(), loadBoardOrigin: async () => 'https://board.example.com' }))
vi.mock('../lib/engineModels', async original => ({
  ...await original<typeof import('../lib/engineModels')>(),
  useEngineModels: () => ({ models: [{ id: 'model-next', name: 'Next', efforts: ['low', 'high'], default_effort: 'low' }], loading: false, fallback: false, source: 'live', engineEfforts: ['low', 'high'] }),
}))

vi.mock('../shellTransport', async original => ({
  ...await original<typeof import('../shellTransport')>(),
  openShell: vi.fn(async () => new FakeShell('palette-shell')),
  restoreShells: vi.fn(async () => []),
}))
const patch = 'diff --git a/app.ts b/app.ts\n--- a/app.ts\n+++ b/app.ts\n@@ -1 +1 @@\n-before\n+after\n'
const views = [
  { kind: 'workspace' as const, path: 'app.ts' },
  { kind: 'artifact' as const, artifact: { id: 'patch', name: 'changes.diff', size: 100, view: 'diff', content_type: 'text/plain' } },
  { kind: 'commit' as const, sha: 'abcdef1234567890' },
  { kind: 'artifact' as const, artifact: { id: 'report', name: 'report.md', size: 42, view: 'markdown', content_type: 'text/markdown' } },
]
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
beforeEach(() => {
  wsMock.resetWsMock()
  localStorage.clear()
  useTranscriptStore.setState({ sessions: {} })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', status: 'idle' })] })
  vi.mocked(openShell).mockReset().mockResolvedValue(new FakeShell('palette-shell'))
  vi.mocked(apiFetch).mockReset().mockImplementation(async url => {
    if (String(url).includes('shell-capability')) return json({ available: true })
    if (String(url).includes('/live')) return json({ can_release_prompt: true })
    if (String(url).includes('/workspace?')) return json({ result: { ok: true, entries: [{ name: 'app.ts', path: 'app.ts', directory: false }], generation: 1, launch_generation: 'launch' }, members: [], runtime: 'sandbox' })
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async url => {
    const path = String(url)
    if (path.includes('/workspace-file')) return json({ path: 'app.ts', view: 'text', content_type: 'text/plain', content_b64: btoa('const app = true') })
    if (path.endsWith('/diff') || path.includes('/patch/raw')) return new Response(patch)
    if (path.endsWith('/raw')) return new Response('# Report\n\nAll checks passed.')
    return json(path.endsWith('/artifacts') ? { artifacts: views.flatMap(v => v.kind === 'artifact' ? [v.artifact] : []) } : {})
  }))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals() })
function mountSession(tabViews = views, previewFirst = false) {
  localStorage.setItem(sessionTabsKey('person-a', 'sess-a'), JSON.stringify({ tabs: tabViews.map((view, index) => ({ id: viewId(view), view, preview: previewFirst && index === 0 })), active: 'chat', unread: 0 }))
  render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
    <RunnerCommands onNewSession={() => {}} />
    <div style={{ height: '100vh' }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
  </CommandPaletteProvider></MemoryRouter>)
}
function oldGuardMatches() {
  return [...document.querySelectorAll('[aria-modal="true"], dialog[open]')].filter(modal =>
    !modal.closest('.palette-dialog') && !modal.closest('[hidden], [aria-hidden="true"]') && getComputedStyle(modal).display !== 'none')
}
// Observe trusted keyboard events without replacing dispatch with fireEvent: Playwright cannot
// expose Firefox's URL bar, so cancellation and continued composer focus are the regression oracle.
async function shortcut(key = 'k', modifier = 'Control') {
  const events: KeyboardEvent[] = []
  const record = (event: KeyboardEvent) => { if (event.key.toLowerCase() === key) events.push(event) }
  window.addEventListener('keydown', record, true)
  try { await userEvent.keyboard(`{${modifier}>}${key}{/${modifier}}`) }
  finally { window.removeEventListener('keydown', record, true) }
  expect(events).toHaveLength(1)
  expect(events[0].isTrusted).toBe(true)
  return events[0]
}
async function fromComposer(surface: string) {
  const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
  expect(composer).toBeVisible()
  composer.focus()
  console.info(`palette baseline after ${surface}: old guard matches ${oldGuardMatches().map(m => m.getAttribute('aria-label') || m.className).join(', ') || 'none'}`)
  for (const key of ['k', 'p']) for (const modifier of ['Control', 'Meta']) {
    const event = await shortcut(key, modifier)
    expect(event.defaultPrevented).toBe(true)
    const dialog = screen.getByRole('dialog', { name: key === 'k' ? 'Command palette' : 'Quick open' })
    expect(document.activeElement).toBe(within(dialog).getByRole('combobox'))
    await userEvent.keyboard('{Escape}')
    expect(composer).toHaveFocus()
    expect(composer.value).toBe('Unsent draft')
  }
}
for (const [width, height] of [[390, 844], [3840, 2160]]) describe(`palette claim at ${width}px`, () => {
  beforeEach(async () => { await page.viewport(width, height) })
  it('claims shortcuts after file, diff, commit, artifact, shell, consent, panel and explorer visits', async () => {
    mountSession()
    await userEvent.fill(document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!, 'Unsent draft')
    for (const name of ['app.ts', 'changes.diff', 'abcdef12', 'report.md']) {
      if (width === 390) {
        await userEvent.click(screen.getByRole('button', { name: '4 open files' }))
        await userEvent.click(screen.getByRole('menuitem', { name }))
      } else await userEvent.click(screen.getByRole('tab', { name: new RegExp(name) }))
      const viewer = await screen.findByRole('region', { name: new RegExp(name === 'abcdef12' ? 'Commit' : name) })
      if (name === 'report.md') await within(viewer).findByRole('heading', { name: 'Report' })
      else await waitFor(() => expect(viewer.textContent).toContain(name === 'app.ts' ? 'const app = true' : 'app.ts'))
      expect(viewer.hasAttribute('aria-modal')).toBe(false)
      expect(viewer.querySelector('[aria-modal="true"]')).toBeNull()
      console.info(`palette baseline during ${name}: old guard matches ${oldGuardMatches().length}`)
      await userEvent.click(width === 390 ? screen.getByRole('button', { name: /← Chat/ }) : screen.getByRole('tab', { name: /Chat/ }))
      await fromComposer(name)
    }
    const challenge = { challenge_id: 'palette-challenge', machine_id: 'fixture-machine', account: 'fixture-account', uid: 1000, shell: '/bin/bash', cwd: '/prepared/project' }
    vi.mocked(openShell).mockRejectedValueOnce(new HostShellConsentRequired(challenge))
    await userEvent.click(await screen.findByRole('button', { name: 'New shell' }))
    const consent = await screen.findByRole('dialog', { name: 'Allow a shell on this machine?' })
    const cancel = within(consent).getByRole('button', { name: 'Cancel' })
    expect(cancel).toHaveFocus()
    console.info(`palette baseline during consent: old guard matches ${oldGuardMatches().length}`)
    // Consent stops bubbling; claiming must happen before its local focus-trap handler.
    for (const key of ['k', 'p']) for (const modifier of ['Control', 'Meta']) {
      expect.soft((await shortcut(key, modifier)).defaultPrevented).toBe(true)
      expect(cancel).toHaveFocus()
      expect(screen.queryByRole('dialog', { name: /Command palette|Quick open/ })).toBeNull()
    }
    await userEvent.click(cancel)
    await fromComposer('shell consent')
    await userEvent.click(await screen.findByRole('button', { name: 'New shell' }))
    await waitFor(() => expect(vi.mocked(openShell)).toHaveBeenCalled())
    await waitFor(() => expect(document.querySelector('.shell-terminal')).toBeVisible())
    expect(document.querySelector('.shell-terminal textarea')).toBeTruthy()
    await userEvent.click(width === 390 ? screen.getByRole('button', { name: /← Chat/ }) : screen.getByRole('tab', { name: /Chat/ }))
    await fromComposer('shell')
    await userEvent.click(screen.getByTestId('rail-toggle'))
    await screen.findByTestId('rail-session')
    if (width === 390) await userEvent.keyboard('{Escape}')
    else await userEvent.click(screen.getByTestId('rail-toggle'))
    await fromComposer('session panel')
    await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
    await screen.findByRole('treeitem', { name: /app.ts/ })
    if (width === 390) await userEvent.keyboard('{Escape}')
    else await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
    await fromComposer('explorer')
  // This journey exercises dozens of trusted browser shortcuts, including Firefox.
  }, 30000)
  it('keeps the review editor link shortcut, selection and focus', async () => {
    function ReviewEditor() {
      const [value, setValue] = useState('selected text')
      return <CommentEditor label="Review comment" value={value} onChange={setValue} />
    }
    render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><ReviewEditor /></CommandPaletteProvider></MemoryRouter>)
    const editor = screen.getByRole('textbox', { name: 'Review comment' }) as HTMLTextAreaElement
    for (const modifier of ['Control', 'Meta']) {
      await userEvent.fill(editor, 'selected text')
      editor.setSelectionRange(0, 8)
      expect((await shortcut('k', modifier)).defaultPrevented).toBe(true)
      expect(editor.value).toBe('[selected](https://) text')
      expect.soft(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull()
      expect.soft(editor).toHaveFocus()
      expect(editor.value.slice(editor.selectionStart, editor.selectionEnd)).toBe('https://')
    }
  })
  it('cancels trusted repeats while a modal blocks opening and while the palette is open', async () => {
    render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
      <div role="dialog" aria-modal="true" aria-label="Blocking modal"><button>Modal control</button></div>
    </CommandPaletteProvider></MemoryRouter>)
    const control = screen.getByRole('button', { name: 'Modal control' })
    control.focus()
    for (const key of ['k', 'p']) for (const modifier of ['Control', 'Meta']) {
      const events: KeyboardEvent[] = []
      const record = (event: KeyboardEvent) => { if (event.key.toLowerCase() === key) events.push(event) }
      window.addEventListener('keydown', record, true)
      try { await userEvent.keyboard(`{${modifier}>}{${key}>2/}{/${modifier}}`) }
      finally { window.removeEventListener('keydown', record, true) }
      expect(events.map(event => event.isTrusted)).toEqual([true, true])
      expect(events.map(event => event.repeat)).toEqual([false, true])
      expect.soft(events.map(event => event.defaultPrevented)).toEqual([true, true])
      expect(control).toHaveFocus()
      expect(screen.queryByRole('dialog', { name: /Command palette|Quick open/ })).toBeNull()
    }
    screen.getByRole('dialog', { name: 'Blocking modal' }).removeAttribute('aria-modal')
    for (const key of ['k', 'p']) for (const modifier of ['Control', 'Meta']) {
      control.focus()
      const events: KeyboardEvent[] = []
      let firstInput: Element | null = null
      const record = (event: KeyboardEvent) => {
        if (event.key.toLowerCase() !== key) return
        events.push(event)
        if (event.repeat) firstInput = document.activeElement
      }
      window.addEventListener('keydown', record, true)
      try { await userEvent.keyboard(`{${modifier}>}{${key}>2/}{/${modifier}}`) }
      finally { window.removeEventListener('keydown', record, true) }
      expect.soft(events.map(event => event.defaultPrevented)).toEqual([true, true])
      const input = screen.getByRole('combobox')
      expect(input).toBe(firstInput)
      expect(input).toHaveFocus()
      await userEvent.keyboard('{Escape}')
      expect(control).toHaveFocus()
    }
  })
  it('blocks opening over a visible modal that overrides inherited hidden visibility', async () => {
    render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
      <div style={{ visibility: 'hidden' }}><section style={{ visibility: 'visible' }} role="dialog" aria-modal="true" aria-label="Visible modal">
        <button>Modal control</button>
      </section></div>
    </CommandPaletteProvider></MemoryRouter>)
    const control = screen.getByRole('button', { name: 'Modal control' })
    expect(control).toBeVisible()
    expect(screen.getByRole('dialog').checkVisibility({ checkOpacity: true, checkVisibilityCSS: true })).toBe(true)
    control.focus()
    for (const key of ['k', 'p']) for (const modifier of ['Control', 'Meta']) {
      expect((await shortcut(key, modifier)).defaultPrevented).toBe(true)
      expect.soft(screen.queryByRole('dialog', { name: /Command palette|Quick open/ })).toBeNull()
      expect.soft(control).toHaveFocus()
    }
  })
  it('ignores retained modals under Activity, CSS and accessibility masks', async () => {
    for (const hiddenBy of ['activity', 'display', 'visibility', 'content-visibility', 'opacity', 'hidden', 'aria-hidden', 'inert']) {
      function RetainedModal() {
        const [visible, setVisible] = useState(true)
        return <><PaletteButton /><div className="agent-composer"><textarea aria-label="Composer" defaultValue="Unsent draft" /></div><button onClick={() => setVisible(false)}>Return to Chat</button>
          <Activity mode={hiddenBy === 'activity' && !visible ? 'hidden' : 'visible'}>
            <div style={!visible ? { display: hiddenBy === 'display' ? 'none' : undefined, visibility: hiddenBy === 'visibility' ? 'hidden' : undefined, contentVisibility: hiddenBy === 'content-visibility' ? 'hidden' : undefined, opacity: hiddenBy === 'opacity' ? 0 : undefined } : undefined} hidden={!visible && hiddenBy === 'hidden'} aria-hidden={!visible && hiddenBy === 'aria-hidden' ? true : undefined} inert={!visible && hiddenBy === 'inert'}>
              <section role="dialog" aria-modal="true" aria-label="Retained viewer">Viewer</section>
            </div>
          </Activity></>
      }
      const view = render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider><RetainedModal /></CommandPaletteProvider></MemoryRouter>)
      await userEvent.click(screen.getByRole('button', { name: 'Return to Chat' }))
      await fromComposer(hiddenBy)
      view.unmount()
    }
  })
})

async function tabKey(key: string, modifier = 'Alt') {
  const events: KeyboardEvent[] = []
  const record = (event: KeyboardEvent) => { if (event.key.toLowerCase() === key.toLowerCase()) events.push(event) }
  window.addEventListener('keydown', record, true)
  const typed = key === '[' ? '[[' : key
  try { await userEvent.keyboard(modifier ? `{${modifier}>}${typed}{/${modifier}}` : `{${key}}`) }
  finally { window.removeEventListener('keydown', record, true) }
  expect(events).toHaveLength(1)
  expect(events[0].isTrusted).toBe(true)
  return events[0]
}

for (const platform of ['Linux x86_64', 'MacIntel']) describe(`global tab shortcuts on ${platform}`, () => {
  it('claims tab navigation from the composer, file, explorer and panel and lists palette shortcuts', async () => {
    await page.viewport(3840, 2160)
    const mockedPlatform = vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
    try {
      mountSession(views, true)
      const modifier = platform === 'MacIntel' ? 'Control' : 'Alt'
      const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
      await userEvent.fill(composer, 'Unsent draft')
      expect((await tabKey('2', modifier)).defaultPrevented).toBe(true)
      const file = await screen.findByRole('region', { name: 'app.ts' })
      expect(file).toHaveFocus()
      expect(composer).not.toBeVisible()
      expect((await tabKey(']', modifier)).defaultPrevented).toBe(true)
      await screen.findByRole('region', { name: 'changes.diff' })
      expect((await tabKey('[', modifier)).defaultPrevented).toBe(true)
      expect(file).toHaveFocus()
      expect(screen.getByRole('tab', { name: /app.ts/ }).closest('.session-tab')).toHaveClass('preview')
      expect((await tabKey('Escape', '')).defaultPrevented).toBe(true)
      expect(composer).toHaveFocus()
      expect(screen.getByRole('tab', { name: /app.ts/ })).toBeTruthy() // Escape returns; it never closes.
      await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
      const search = await screen.findByRole('combobox', { name: 'Search workspace files' })
      search.focus()
      expect((await tabKey('4', modifier)).defaultPrevented).toBe(true)
      await screen.findByRole('region', { name: 'Commit abcdef1' })
      if (screen.getByTestId('rail-toggle').getAttribute('aria-expanded') !== 'true') await userEvent.click(screen.getByTestId('rail-toggle'))
      await waitFor(() => expect(screen.getByTestId('rail-session')).toBeVisible())
      within(screen.getByRole('tablist', { name: 'Session panel views' })).getByRole('tab', { selected: true }).focus()
      expect((await tabKey('5', modifier)).defaultPrevented).toBe(true)
      const artifact = await screen.findByRole('region', { name: 'report.md' })
      expect(artifact).toHaveFocus()
      expect((await tabKey('w', modifier)).defaultPrevented).toBe(true)
      expect(screen.queryByRole('tab', { name: /report.md/ })).toBeNull()
      expect((await tabKey('1', modifier)).defaultPrevented).toBe(true)
      expect(composer).toHaveFocus()
      expect(composer.value).toBe('Unsent draft')
      expect((await tabKey('w', modifier)).defaultPrevented).toBe(true)
      expect(screen.getByRole('tab', { name: /Chat/ })).toHaveAttribute('aria-selected', 'true')
      // Browser close stays native on Windows/Linux. macOS Control+W is the owned tab key.
      if (platform !== 'MacIntel') {
        const nativeClose = new KeyboardEvent('keydown', { key: 'w', ctrlKey: true, bubbles: true, cancelable: true })
        composer.dispatchEvent(nativeClose)
        expect(nativeClose.defaultPrevented).toBe(false)
      }
      await userEvent.keyboard('{Control>}k{/Control}')
      const input = screen.getByRole('combobox', { name: /^Search commands/ })
      await userEvent.fill(input, '>Back to Chat')
      const back = await screen.findByRole('option', { name: /Back to Chat/ })
      expect(back.querySelector('kbd')?.textContent).toBe(`Esc / ${modifier}+1`)
      await userEvent.fill(input, '>Go to tab 3')
      expect((await screen.findByRole('option', { name: /Go to tab 3/ })).querySelector('kbd')?.textContent).toBe(`${modifier}+3`)
      await userEvent.keyboard('{Enter}')
      expect(await screen.findByRole('region', { name: 'changes.diff' })).toHaveFocus()
    } finally { mockedPlatform.mockRestore() }
  })
})

for (const platform of ['Linux x86_64', 'MacIntel']) describe(`tab shortcut ownership on ${platform}`, () => {
  it('addresses tabs 2 through 9 in current order and follows reordering', async () => {
    await page.viewport(3840, 2160)
    const mockedPlatform = vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
    try {
      const tabViews = Array.from({ length: 9 }, (_, i) => ({ kind: 'workspace' as const, path: `file-${i + 2}.ts` }))
      mountSession(tabViews)
      const modifier = platform === 'MacIntel' ? 'Control' : 'Alt'
      document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!.focus()
      for (let number = 2; number <= 9; number++) {
        expect((await tabKey(String(number), modifier)).defaultPrevented).toBe(true)
        expect(await screen.findByRole('region', { name: `file-${number}.ts` })).toHaveFocus()
      }
      const ninth = screen.getByRole('tab', { name: /file-9.ts/ }); ninth.focus()
      await userEvent.keyboard('{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}')
      await tabKey('9', modifier)
      expect(await screen.findByRole('region', { name: 'file-8.ts' })).toHaveFocus()
      await tabKey(']', modifier)
      expect(await screen.findByRole('region', { name: 'file-10.ts' })).toHaveFocus()
      await tabKey(']', modifier)
      expect(document.querySelector('.agent-composer textarea')).toHaveFocus()
    } finally { mockedPlatform.mockRestore() }
  })

  it('respects text fields, editors, terminals, IME, menus, popovers and unavailable slots', async () => {
    await page.viewport(3840, 2160)
    const mockedPlatform = vi.spyOn(navigator, 'platform', 'get').mockReturnValue(platform)
    try {
      mountSession()
      const modifier = platform === 'MacIntel' ? 'Control' : 'Alt'
      const mods = modifier === 'Control' ? { ctrlKey: true } : { altKey: true }
      const chat = screen.getByRole('tab', { name: /Chat/ })
      const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
      composer.focus()
      await tabKey('Escape', '')
      expect(chat).toHaveAttribute('aria-selected', 'true')
      await tabKey('2', modifier)
      const file = await screen.findByRole('region', { name: 'app.ts' })
      function dispatch(target: HTMLElement, key: string, init: KeyboardEventInit = {}) {
        const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, composed: true, ...init })
        target.dispatchEvent(event)
        return event
      }
      // Synthetic events avoid sending native browser/tab-close or terminal control sequences.
      const field = document.createElement('textarea'); file.appendChild(field); field.focus()
      expect(dispatch(field, 'Escape').defaultPrevented).toBe(false)
      expect(screen.getByRole('tab', { name: /app.ts/ })).toHaveAttribute('aria-selected', 'true')
      for (const selector of ['xterm', 'terminal-dom-surface', 'monaco-editor', 'cm-editor', 'CodeMirror', 'ace_editor', 'review-editor', 'tiptap', 'opt-out']) {
        const surface = document.createElement('div')
        surface.className = selector
        if (selector === 'opt-out') surface.dataset.paletteShortcuts = 'ignore'
        const input = document.createElement('textarea'); surface.appendChild(input); file.appendChild(surface); input.focus()
        for (const key of ['1', '2', '9', '[', ']', 'w', 'Escape']) {
          expect(dispatch(input, key, key === 'Escape' ? {} : mods).defaultPrevented, selector).toBe(false)
          expect(screen.getByRole('tab', { name: /app.ts/ })).toHaveAttribute('aria-selected', 'true')
        }
        surface.remove()
      }
      field.focus()
      expect(dispatch(field, '1', { ...mods, isComposing: true }).defaultPrevented).toBe(false)
      expect(dispatch(field, '1', { ...mods, keyCode: 229 }).defaultPrevented).toBe(false)
      expect(dispatch(field, '1', { ...mods, shiftKey: true }).defaultPrevented).toBe(false)
      expect(dispatch(field, '1', modifier === 'Control' ? { altKey: true } : { ctrlKey: true }).defaultPrevented).toBe(false)
      const claimed = new KeyboardEvent('keydown', { key: '1', ...mods, bubbles: true, cancelable: true }); claimed.preventDefault(); field.dispatchEvent(claimed)
      expect(screen.getByRole('tab', { name: /app.ts/ })).toHaveAttribute('aria-selected', 'true')
      expect(dispatch(field, '9', mods).defaultPrevented).toBe(true)
      expect(dispatch(field, ']', { ...mods, repeat: true }).defaultPrevented).toBe(true)
      expect(screen.getByRole('tab', { name: /app.ts/ })).toHaveAttribute('aria-selected', 'true')
      field.addEventListener('keydown', event => event.stopPropagation(), { once: true })
      expect(dispatch(field, '1', mods).defaultPrevented).toBe(true)
      expect(screen.getByRole('tab', { name: /app.ts/ })).toHaveAttribute('aria-selected', 'true')
      field.remove()
      const menu = document.createElement('div'); menu.setAttribute('role', 'menu')
      const menuItem = document.createElement('button'); menu.appendChild(menuItem); file.appendChild(menu); menuItem.focus()
      expect(dispatch(menuItem, 'Escape').defaultPrevented).toBe(false)
      expect(dispatch(menuItem, '1', mods).defaultPrevented).toBe(true)
      expect(screen.getByRole('tab', { name: /app.ts/ })).toHaveAttribute('aria-selected', 'true')
      menu.remove()
      await userEvent.click(screen.getByTestId('stop-session'))
      const popover = await screen.findByRole('alertdialog', { name: 'End this session?' })
      const cancel = within(popover).getByRole('button', { name: 'Cancel' })
      expect((await tabKey('1', modifier)).defaultPrevented).toBe(true)
      expect(cancel).toHaveFocus()
      expect(screen.getByRole('tab', { name: /app.ts/ })).toHaveAttribute('aria-selected', 'true')
      await userEvent.keyboard('{Escape}')
      expect(screen.queryByRole('alertdialog')).toBeNull()
      expect(screen.getByRole('tab', { name: /app.ts/ })).toHaveAttribute('aria-selected', 'true')
      file.focus()
      await tabKey('1', modifier)
      expect(composer).toHaveFocus()
      await tabKey('[', modifier)
      expect(await screen.findByRole('region', { name: 'report.md' })).toHaveFocus()
      await tabKey(']', modifier)
      expect(composer).toHaveFocus()
    } finally { mockedPlatform.mockRestore() }
  })
})
