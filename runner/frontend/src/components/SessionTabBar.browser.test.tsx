import { beforeEach, afterEach, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { page, userEvent } from 'vitest/browser'
import { useTranscriptStore, type AgentEvent } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { sessionTabDragType } from './SessionGroups'
import { sessionTabsKey, viewId } from '../hooks/useSessionTabs'
import { openSessionFile } from '../lib/openSessionFile'
import SessionList from './SessionList'
import CommandPaletteProvider from './CommandPalette'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import { invalidateChatTransport } from '../lib/chatTransport'
import '../index.css'
import '@blerglab/chat/blerg.css'

const shellCounter = vi.hoisted(() => ({ value: 0 }))
vi.mock('../shellTransport', async original => {
  const actual = await original<typeof import('../shellTransport')>()
  const { FakeShell } = await import('../test/shellMock')
  return { ...actual, openShell: vi.fn(async () => new FakeShell(`tab-shell-${++shellCounter.value}`)), restoreShells: vi.fn(async (...args: Parameters<typeof actual.restoreShells>) => args[3] ?? []) }
})
vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../authClient', async original => ({ ...await original<typeof import('../authClient')>(), getAccessToken: () => `x.${btoa(JSON.stringify({ sub: 'person-a', exp: 9999999999 }))}.x`, ensureFreshToken: async () => true }))
const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))
vi.mock('../lib/boardLink', async original => ({ ...await original<typeof import('../lib/boardLink')>(), loadBoardOrigin: async () => 'https://board.example.com' }))
vi.mock('../lib/engineModels', async original => ({
  ...await original<typeof import('../lib/engineModels')>(),
  useEngineModels: () => ({ models: [{ id: 'model-next', name: 'Next', efforts: ['low', 'high'], default_effort: 'low' }], loading: false, fallback: false, source: 'live', engineEfforts: ['low', 'high'] }),
}))
const fileShotDir = import.meta.env.VITE_FILE_TAB_SHOTS_DIR
const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_TAB_SHOT_DIR
const rootEntries = [
  { name: 'app', path: 'app', directory: true, repository: true, member_id: 'app' },
  { name: 'docs', path: 'docs', directory: true },
  { name: 'README.md', path: 'README.md', directory: false },
  { name: 'package.json', path: 'package.json', directory: false },
]
const sources: Record<string, [string, string]> = {
  'main.go': ['text', `package main

import (
    "fmt"
    "net/http"
)

// The file viewer keeps long source lines inside the code scroller.
const description = "${'A message from the session workspace. '.repeat(12)}"

func main() {
    http.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/plain")
        fmt.Fprintln(w, description)
    })
    fmt.Println("Ready to accept requests")
}
`],
  'main.ts': ['text', 'const value: string = "hello"'],
  'main.js': ['text', 'const value = "hello"'],
  'main.py': ['text', 'def hello():\n  return "hello"'],
  'main.sh': ['text', 'echo "hello"'],
  'main.yaml': ['text', 'name: "hello"'],
  'main.json': ['json', '{"name": "hello"}'],
  'go.mod': ['text', 'module example.com/demo\n\ngo 1.26\nrequire example.com/lib v1.2.3'],
  'go.sum': ['text', 'example.com/lib v1.2.3 h1:abc='],
  'Dockerfile': ['text', 'FROM alpine:3.20\nRUN echo hello'],
  'Dockerfile.dev': ['text', 'FROM alpine:3.20'],
  'build.dockerfile': ['text', 'FROM alpine:3.20'],
  'Makefile': ['text', 'build:\n\techo "hello"'],
  'GNUmakefile': ['text', 'build:\n\techo "hello"'],
  'rules.mk': ['text', 'build:\n\techo "hello"'],
  'notes.md': ['markdown', '# Hello\n\n**world**'],
  'chart.mmd': ['text', 'graph TD\n A-->B'],
  'chart.vl.json': ['json', '{"mark":"bar","data":{"values":[]}}'],
  'page.html': ['html', '<h1>Hello</h1>'],
  'table.csv': ['csv', 'name,count\nhello,2'],
}
const PATCH = 'diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,2 +1,2 @@\n package main\n-func main() {}\n+func main() { println("hello") }\n'
const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })
beforeEach(() => {
  shellCounter.value = 0
  localStorage.clear()
  document.documentElement.setAttribute('data-theme', 'dark')
  useTranscriptStore.setState({ sessions: {} })
  const transcript = useTranscriptStore.getState()
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'dock-user', seq: 1, ts: '2026-10-24T12:00:00Z', kind: 'user_message', payload: { text: 'Add the explorer beside the chat and keep the published files in the panel.', source: 'chat' } } as AgentEvent)
  transcript.ingest({ session_id: 'sess-a', client_event_id: 'dock-assistant', seq: 2, ts: '2026-10-24T12:00:01Z', kind: 'assistant_text', payload: { text: 'The workspace tree is available from the folder control on the left activity rail. You can resize it with the divider, browse repositories with the keyboard, and open files in the shared viewer.', done: true } } as AgentEvent)
  transcript.ingestReplayDone('sess-a', { lastSeq: 2, hasMore: false })
  useSessionStore.setState({ daemons: [makeDaemon()], sessions: [makeSession({ kind: 'agent', status: 'idle', title: 'Build the workspace explorer' })] })
  localStorage.setItem(RAIL_PREF_KEY, '1')
  localStorage.setItem(RAIL_TAB_KEY, 'files')
  apiFetch.mockReset().mockImplementation(async (url: string) => {
    if (url.includes('shell-capability')) return json({ available: true })
    if (url.includes('/live')) return json({ can_release_prompt: true })
    if (url.includes('/workspace?')) {
      const query = new URL(url, 'https://example.com').searchParams
      const path = query.get('path'), op = query.get('op')
      return json({ result: { ok: true, launch_generation: 'launch', generation: 1, entries: op === 'search' ? [rootEntries[2]] : path === 'app' ? [{ name: 'src', path: 'app/src', directory: true }, { name: 'index.ts', path: 'app/index.ts', directory: false }, { name: 'assets', path: 'app/assets', directory: true }] : path === 'app/src' ? [{ name: 'component.ts', path: 'app/src/component.ts', directory: false }] : path === 'app/assets' ? [{ name: 'logo.svg', path: 'app/assets/logo.svg', directory: false }] : rootEntries, truncated: false, ...(op === 'status' ? { status: { branch: 'main', changed: 3, untracked: 1, files: [{ path: 'index.ts', code: 'M' }, { path: 'src/component.ts', code: 'A' }, { path: 'removed.txt', code: 'D' }, { path: 'assets', code: 'U', directory: true }] } } : {}) }, members: [{ id: 'app', name: 'app', path: 'app' }], runtime: 'host' })
    }
    return json({})
  })
  vi.stubGlobal('fetch', vi.fn(async (url: string) => String(url).includes('/workspace-file')
    ? (() => { const path = new URL(String(url), location.origin).searchParams.get('path') ?? 'README.md'; const [view, text] = (path === 'remember.md' ? sources['notes.md'] : sources[path]) ?? ['text', 'Workspace viewer fixture']; return json({ path, view, content_type: 'text/plain', content_b64: btoa(text) }) })()
    : String(url).endsWith('/diff') ? new Response(PATCH)
    : String(url).endsWith('/raw') ? new Response(String(url).includes('/patch/') ? 'diff --git a/app.ts b/app.ts\n--- a/app.ts\n+++ b/app.ts\n@@ -1 +1 @@\n-before\n+after\n' : '# Deployment notes\n\nAll checks passed.')
    : json(String(url).endsWith('/artifacts') ? { artifacts: [{ id: 'report', name: 'report.md', size: 42, view: 'markdown', content_type: 'text/markdown', version: 1, latest_version: 1 }, { id: 'patch', name: 'changes.diff', size: 100, view: 'diff', content_type: 'text/plain' }] } : [])))
})
afterEach(() => { invalidateChatTransport(); vi.unstubAllGlobals(); document.documentElement.removeAttribute('data-theme') })
function mount(width: number) {
  return render(<MemoryRouter initialEntries={['/sessions/sess-a']}><CommandPaletteProvider>
    <div data-testid="dock-layout" style={{ display: 'flex', height: '100vh' }}>
      {width >= 900 && <SessionList variant="sidebar" onNewSession={() => {}} />}
      <div style={{ flex: 1, minWidth: 0 }}><Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes></div>
    </div>
  </CommandPaletteProvider></MemoryRouter>)
}

async function drawShell(sample = '/workspace/demo-app $ git diff --stat\r\n src/app.ts | 2 +-\r\n 1 file changed, 1 insertion(+), 1 deletion(-)\r\n/workspace/demo-app $ ') {
  const { openShell } = await import('../shellTransport')
  const { FakeShell } = await import('../test/shellMock')
  const result = vi.mocked(openShell).mock.results.at(-1)
  if (result?.type !== 'return') return
  const shell = await result.value
  if (shell instanceof FakeShell) {
    await waitFor(() => expect(shell.output).toBeDefined())
    act(() => shell.emit(sample))
  }
}

for (const [width, height] of [[390, 844], [1440, 900], [3840, 2160]]) {
  it(`opens reusable previews, keeps tabs and retains the composer at ${width}`, async () => {
    await page.viewport(width, height)
    const view = mount(width)
    const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
    expect(composer).toBeTruthy()
    expect(screen.queryByTestId('session-more')).toBeNull()
    const explorer = screen.getByRole('button', { name: 'Toggle explorer' })
    if (width > 390) expect(explorer.closest('nav')?.getAttribute('aria-label')).toBe('Workspace')
    await userEvent.click(explorer)
    expect(document.querySelector('.explorer-hint')).toHaveTextContent('Double-click to keep it open.')
    expect(document.querySelector('.explorer-hint')).not.toHaveTextContent('focus it')
    await userEvent.click(await screen.findByRole('treeitem', { name: /README.md/ }))
    await screen.findAllByTestId('workspace-code')
    expect(document.querySelector('[role="dialog"][aria-modal="true"]')).toBeNull()
    const filePanel = screen.getByRole('tabpanel', { name: /README.md/ })
    expect(filePanel.getBoundingClientRect().height).toBeGreaterThan(200)
    if (width > 390) {
      await userEvent.click(screen.getByRole('treeitem', { name: /package.json/ }))
      await waitFor(() => expect(screen.queryByRole('tab', { name: /README.md/ })).toBeNull())
      await userEvent.click(screen.getByRole('treeitem', { name: /README.md/ }))
    }
    await userEvent.dblClick(document.querySelector<HTMLButtonElement>('.session-tab.active [role="tab"]')!)
    expect(screen.queryByRole('button', { name: 'Keep open' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
    if (width === 390) await userEvent.click(explorer)
    await userEvent.click(await screen.findByRole('treeitem', { name: /package.json/ }))
    await screen.findAllByTestId('workspace-code')
    if (width > 390) {
      expect(screen.getAllByRole('tab', { name: /README.md|package.json/ })).toHaveLength(2)
      await userEvent.dblClick(screen.getByRole('tab', { name: /package.json/ }))
    }
    if (width === 390) {
      act(() => history.back())
      await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('README.md'))
      act(() => history.forward())
      await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('package.json'))
    } else {
      const last = screen.getByRole('tab', { name: /package.json/ })
      last.focus()
      await userEvent.keyboard('{Alt>}{Shift>}{ArrowLeft}{/Shift}{/Alt}')
      expect(screen.getAllByRole('tab').slice(0, 3).map(t => t.dataset.tabId)).toEqual(['chat', 'workspace:package.json', 'workspace:README.md'])
      await userEvent.dragAndDrop(screen.getByRole('tab', { name: /README.md/ }).closest<HTMLElement>('.session-tab')!, last.closest<HTMLElement>('.session-tab')!)
      expect(screen.getAllByRole('tab').slice(0, 3).map(t => t.dataset.tabId)).toEqual(['chat', 'workspace:README.md', 'workspace:package.json'])
    }
    if (shotDir && width > 390) { await userEvent.click(await screen.findByRole('button', { name: 'New shell' })); await drawShell() }
    if (shotDir) await page.screenshot({ path: `${shotDir}/tabs-${width}x${height}.png` })
    const store = useTranscriptStore.getState()
    act(() => store.ingest({ session_id: 'sess-a', client_event_id: 'new-output', seq: 3, ts: '2026-10-25T12:00:00Z', kind: 'assistant_text', payload: { text: 'New agent output', done: true } } as AgentEvent))
    await screen.findByLabelText('New agent output')
    await userEvent.click(width === 390 ? screen.getByRole('button', { name: /← Chat/ }) : screen.getByRole('tab', { name: /Chat/ }))
    expect(document.querySelector('.agent-composer textarea')).toBe(composer)
    if (width === 390) expect(composer).not.toHaveFocus()
    else expect(document.activeElement).toBe(composer)
    expect(screen.queryByLabelText('New agent output')).toBeNull()
    expect(openSessionFile({ session: 'another-session', account: 'person-a', path: 'other.ts' })).toBe(false)
    act(() => { expect(openSessionFile({ session: 'sess-a', account: 'person-a', path: 'quick.ts' })).toBe(true) })
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('quick.ts'))
    await userEvent.click(screen.getByRole('button', { name: 'Close quick.ts' }))
    view.unmount()
    mount(width)
    if (width > 390) expect(screen.getByRole('tab', { name: /README.md/ })).toBeTruthy()
    else {
      await userEvent.click(screen.getByRole('button', { name: /open files?/ }))
      await userEvent.click(screen.getByRole('menuitem', { name: 'README.md' }))
      await screen.findAllByTestId('workspace-code')
      await userEvent.click(screen.getByRole('button', { name: 'Toggle explorer' }))
      if (shotDir) await page.screenshot({ path: `${shotDir}/explorer-390x844.png` })
    }
    if (width === 390) {
      await userEvent.keyboard('{Escape}')
      const shellToggle = await screen.findByRole('button', { name: 'New shell' })
      await userEvent.click(shellToggle)
      const terminal = await waitFor(() => { const node = document.querySelector('.shell-terminal textarea'); expect(node).toBeTruthy(); return node! })
      if (shotDir) { await drawShell(); await page.screenshot({ path: `${shotDir}/shell-390x844.png` }) }
      await userEvent.click(screen.getByRole('button', { name: /← Chat/ }))
      await userEvent.click(screen.getByRole('button', { name: /open tab/ }))
      await userEvent.click(screen.getByRole('menuitem', { name: 'Shell' }))
      expect(document.querySelector('.shell-terminal textarea')).toBe(terminal)
      await userEvent.click(screen.getByRole('button', { name: /← Chat/ }))
      await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
      const palette = screen.getByRole('dialog', { name: 'Command palette' })
      await userEvent.fill(within(palette).getByRole('combobox'), '>End session')
      await userEvent.keyboard('{ArrowDown}{Enter}')
      const confirm = screen.getByRole('alertdialog', { name: 'End this session?' })
      expect(within(confirm).getByRole('button', { name: 'Cancel' })).toHaveFocus()
      await userEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
      expect(apiFetch.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false)
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
  })
}

it('opens published Markdown and diffs in tabs with their existing renderers', async () => {
  await page.viewport(1440, 900); mount(1440)
  const rows = await screen.findAllByTestId('artifact-row')
  await userEvent.click(rows.find(row => row.textContent?.includes('report.md'))!.querySelector<HTMLButtonElement>('button[aria-label^="View"]')!)
  await screen.findByRole('heading', { name: 'Deployment notes' })
  expect(screen.getByRole('tab', { name: /report.md/ })).toHaveAttribute('aria-selected', 'true')
  await userEvent.dblClick(document.querySelector<HTMLButtonElement>('.session-tab.active [role="tab"]')!)
  expect(screen.queryByRole('button', { name: 'Exit tab focus' })).toBeNull()
  await userEvent.click(rows.find(row => row.textContent?.includes('changes.diff'))!.querySelector<HTMLButtonElement>('button[aria-label^="View"]')!)
  await screen.findByTestId('diff-view')
  expect(screen.getByRole('tab', { name: /changes.diff/ })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('tab', { name: /report.md/ })).toBeTruthy()
  expect(screen.queryByRole('dialog')).toBeNull()
  await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
  await userEvent.fill(screen.getByRole('combobox', { name: /^Search commands/ }), '>Focus mode')
  await userEvent.keyboard('{Enter}')
  expect(screen.getByRole('region', { name: 'changes.diff' })).toHaveFocus()
  expect(screen.getByRole('tab', { name: /changes.diff/ })).toHaveAttribute('aria-selected', 'true')
})

async function openFile(path: string, keep = false) {
  act(() => { expect(openSessionFile({ session: 'sess-a', account: 'person-a', path })).toBe(true) })
  await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe(path))
  await screen.findAllByTestId('workspace-code')
  if (keep) await userEvent.dblClick(document.querySelector<HTMLButtonElement>('.session-tab.active [role="tab"]')!)
}

for (const [width, height] of [[1440, 900], [3840, 2160]]) {
  it(`keeps consecutive arrow navigation in the tablist and preserves the draft at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
    await userEvent.fill(composer, 'Unsent draft')
    await openFile('one.ts', true)
    const one = screen.getByRole('tab', { name: /one.ts/ })
    const chat = screen.getByRole('tab', { name: /Chat/ })
    one.focus()
    await userEvent.keyboard('{ArrowLeft}')
    expect(chat).toHaveAttribute('aria-selected', 'true')
    expect(chat).toHaveFocus()
    await userEvent.keyboard('{ArrowRight}')
    expect(one).toHaveFocus()
    await userEvent.keyboard('{Home}')
    expect(chat).toHaveFocus()
    await userEvent.keyboard('{End}')
    expect(one).toHaveFocus()
    await userEvent.click(chat)
    expect(composer).toHaveFocus()
    expect(composer.value).toBe('Unsent draft')
  })

  for (const control of ['Delete', 'tab close']) {
    it(`restores focus to the surviving tab after ${control} at ${width}`, async () => {
      await page.viewport(width, height); mount(width)
      await openFile('one.ts', true)
      await openFile('two.ts', true)
      const two = screen.getByRole('tab', { name: /two.ts/ })
      if (control === 'Delete') { two.focus(); await userEvent.keyboard('{Delete}') }
      else if (control === 'Escape') { screen.getByRole('region', { name: 'two.ts' }).focus(); await userEvent.keyboard('{Escape}') }
      else await userEvent.click(screen.getByRole('button', { name: 'Close two.ts' }))
      const one = screen.getByRole('tab', { name: /one.ts/ })
      expect(one).toHaveAttribute('aria-selected', 'true')
      expect(one).toHaveFocus()
      await userEvent.keyboard('{ArrowLeft}')
      expect(screen.getByRole('tab', { name: /Chat/ })).toHaveFocus()
      await userEvent.keyboard('{ArrowRight}{Delete}')
      expect(screen.queryByRole('tab', { name: /one.ts/ })).toBeNull()
      expect(screen.getByRole('tab', { name: /Chat/ })).toHaveFocus()
    })
  }
}

for (const replacePreview of [false, true]) {
  it(`reconciles phone Back and Forward after closing files (replace preview: ${replacePreview})`, async () => {
    await page.viewport(390, 844); mount(390)
    await openFile('one.ts', true)
    await openFile('two.ts', true)
    if (replacePreview) await openFile('discarded-preview.ts')
    await openFile('three.ts')
    await userEvent.click(screen.getByRole('button', { name: 'Close three.ts' }))
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('two.ts'))
    expect(history.state.sessionTab.id).toBe('workspace:two.ts')
    await userEvent.click(screen.getByRole('button', { name: 'Back to Chat' }))
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('one.ts'))
    act(() => history.forward())
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('two.ts'))
    await userEvent.click(screen.getByRole('button', { name: 'Close two.ts' }))
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('one.ts'))
    await userEvent.click(screen.getByRole('button', { name: 'Back to Chat' }))
    await waitFor(() => expect(document.querySelector('.agent-composer textarea')).toBeVisible())
    act(() => history.forward())
    await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('one.ts'))
  })
}

it('supplies real labeled tabpanels for terminal sessions', async () => {
  await page.viewport(1440, 900)
  useSessionStore.setState({ sessions: [makeSession({ kind: 'terminal', status: 'idle' })] })
  mount(1440)
  function selectedPanel() {
    const tab = document.querySelector<HTMLButtonElement>('.session-tab.active [role="tab"]')!
    const panel = document.getElementById(tab.getAttribute('aria-controls')!)
    expect(panel).toBeTruthy()
    expect(panel).toHaveAttribute('role', 'tabpanel')
    expect(panel).toHaveAccessibleName()
    expect(panel).toBeVisible()
  }
  selectedPanel()
  await openFile('one.ts')
  selectedPanel()
  expect(document.getElementById('session-view-chat')).not.toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: 'Close one.ts' }))
  selectedPanel()
  expect(screen.getByRole('tab', { name: /Chat/ })).toHaveFocus()
})

for (const [width, height] of [[390, 844], [3840, 2160]]) {
  it(`highlights file names and keeps Raw choice in each tab at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    for (const [path, [view, text]] of Object.entries(sources)) {
      act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path }) })
      const region = await screen.findByRole('region', { name: path })
      if (view !== 'text' || path.endsWith('.mmd')) await userEvent.click(await within(region).findByRole('button', { name: 'Raw' }))
      await waitFor(() => expect(region.querySelector('[class*="hljs-"]'), path).not.toBeNull(), { timeout: 10000 })
      expect(region.querySelector('code')?.textContent).toBe(text)
      const token = region.querySelector<HTMLElement>('[class*="hljs-"]')!
      expect(getComputedStyle(token).color).not.toBe(getComputedStyle(region.querySelector('code')!).color)
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    }
    // Kept tabs stay mounted; their display preference must survive switching.
    act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'remember.md' }) })
    const notes = await screen.findByRole('region', { name: 'remember.md' })
    await userEvent.click(await within(notes).findByRole('button', { name: 'Raw' }))
    act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'main.go' }) })
    await screen.findByRole('region', { name: 'main.go' })
    act(() => { openSessionFile({ session: 'sess-a', account: 'person-a', path: 'remember.md' }) })
    expect(await within(notes).findByRole('button', { name: 'Rendered' })).toBeTruthy()
    await userEvent.click(within(notes).getByRole('button', { name: 'Rendered' }))
    expect(await within(notes).findByRole('heading', { name: 'Hello' })).toBeTruthy()
  })
}

it('middle-closes file, diff, commit and preview tabs while keeping Chat and focus at 3840', async () => {
  await page.viewport(3840, 2160)
  const views = [
    { kind: 'workspace' as const, path: 'one.ts' },
    { kind: 'artifact' as const, artifact: { id: 'patch', name: 'changes.diff', size: 100, view: 'diff', content_type: 'text/plain' } },
    { kind: 'commit' as const, sha: 'abcdef1234567890' },
    { kind: 'workspace' as const, path: 'preview.ts' },
  ]
  const tabs = views.map((view, i) => ({ id: viewId(view), view, preview: i === 3 }))
  localStorage.setItem(sessionTabsKey('person-a', 'sess-a'), JSON.stringify({ tabs, active: tabs[3].id, unread: 0 }))
  mount(3840)
  const chat = screen.getByRole('tab', { name: /Chat/ })
  const state = history.state
  for (const target of [chat, screen.getByRole('tablist', { name: 'Session tabs' })]) {
    for (const type of ['mousedown', 'auxclick']) {
      const event = new MouseEvent(type, { button: 1, bubbles: true, cancelable: true })
      target.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(true)
    }
  }
  await userEvent.click(chat, { button: 'middle' })
  expect(chat).toHaveAttribute('aria-selected', 'false')
  for (const [name, survivor] of [['preview.ts', 'abcdef12'], ['abcdef12', 'changes.diff'], ['changes.diff', 'one.ts'], ['one.ts', 'Chat']]) {
    const tab = screen.getByRole('tab', { name: new RegExp(name) })
    const down = new MouseEvent('mousedown', { button: 1, bubbles: true, cancelable: true })
    tab.dispatchEvent(down)
    expect(down.defaultPrevented).toBe(true)
    await userEvent.click(tab, { button: 'middle' })
    expect(screen.queryByRole('tab', { name: new RegExp(name) })).toBeNull()
    const selected = screen.getByRole('tab', { name: new RegExp(survivor) })
    expect(selected).toHaveAttribute('aria-selected', 'true')
    expect(selected).toHaveFocus()
  }
  expect(history.state).toEqual(state)
})
it('middle-closes a kept phone tab with the close-button history rules at 390', async () => {
  await page.viewport(390, 844); mount(390)
  await openFile('one.ts', true)
  await openFile('two.ts', true)
  const depth = history.state.sessionTab.depth
  await userEvent.click(screen.getByRole('button', { name: '2 open files' }))
  await userEvent.click(screen.getByRole('menuitem', { name: 'two.ts' }), { button: 'middle' })
  await waitFor(() => expect(document.querySelector('.hosted-view:not([hidden]) h2')?.textContent).toBe('one.ts'))
  expect(history.state.sessionTab.id).toBe('workspace:one.ts')
  expect(history.state.sessionTab.depth).toBe(depth)
  // Closing the menu returns focus to content; Escape then returns to Chat.
  await userEvent.keyboard('{Escape}')
  await waitFor(() => expect(document.querySelector('.agent-composer textarea')).toBeVisible())
})

it('middle-closes an inactive tab without activating it', async () => {
  await page.viewport(3840, 2160); mount(3840)
  await openFile('one.ts', true); await openFile('two.ts', true)
  await userEvent.click(screen.getByRole('tab', { name: /one.ts/ }), { button: 'middle' })
  expect(screen.queryByRole('tab', { name: /one.ts/ })).toBeNull()
  expect(screen.getByRole('tab', { name: /two.ts/ })).toHaveAttribute('aria-selected', 'true')
  expect(screen.getByRole('tab', { name: /two.ts/ })).toHaveFocus()
})

for (const width of [390, 3840]) it(`keeps one Insights entry and no duplicate in the session panel at ${width}`, async () => {
  await page.viewport(width, 900); localStorage.setItem(RAIL_TAB_KEY, 'live'); mount(width)
  if (width === 390) await userEvent.click(screen.getByTestId('rail-toggle'))
  await screen.findByTestId('rail-session')
  expect(within(screen.getByTestId('rail-session')).queryByRole('button', { name: 'Insights' })).toBeNull()
  if (width === 3840) expect(screen.getAllByRole('button', { name: 'Insights' })).toHaveLength(1)
  else {
    await userEvent.keyboard('{Escape}')
    await userEvent.click(screen.getByRole('button', { name: 'Search commands and files' }))
    await userEvent.fill(screen.getByRole('combobox', { name: /^Search commands/ }), '>Session insights')
    expect(screen.getAllByRole('option', { name: 'Session insights' })).toHaveLength(1)
  }
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
})

for (const [width, height] of [[390, 844], [1024, 900], [3840, 2160]]) {
  it(`gives file tabs one toolbar and the full chat height at ${width}`, async () => {
    await page.viewport(width, height); mount(width)
    const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
    await userEvent.fill(composer, 'Draft kept while reading')
    composer.setSelectionRange(6, 10)
    await openFile('main.go')
    const region = screen.getByRole('region', { name: 'main.go' })
    expect(composer).not.toBeVisible()
    expect(within(region).queryByRole('button', { name: 'Keep open' })).toBeNull()
    expect(within(region).queryByRole('button', { name: 'Close' })).toBeNull()
    const toolbar = within(region).getByRole('toolbar', { name: 'File actions' })
    const code = within(region).getByTestId('workspace-code')
    const top = screen.getByRole('navigation', { name: width === 390 ? 'Session navigation' : 'Workspace' }).getBoundingClientRect().bottom
    expect(toolbar.getBoundingClientRect().top).toBeCloseTo(top, 0)
    expect(code.getBoundingClientRect().top).toBeCloseTo(toolbar.getBoundingClientRect().bottom, 0)
    expect(toolbar.getBoundingClientRect().height).toBeLessThanOrEqual(40)
    if (width === 390) expect(code.scrollWidth).toBeGreaterThan(code.clientWidth)
    expect(region.getBoundingClientRect().bottom).toBeCloseTo(screen.getByTestId('session-chat-column').getBoundingClientRect().bottom, 0)
    for (const name of ['Copy path', 'Download', 'Save a copy to Files']) {
      const action = within(toolbar).getByRole('button', { name })
      expect(action.title).toBe(name)
      expect(action.tabIndex).toBe(0)
    }
    region.focus()
    await userEvent.keyboard('{Tab}')
    expect(within(toolbar).getByRole('button', { name: 'Copy main.go path' })).toHaveFocus()
    await userEvent.keyboard('{Tab}')
    expect(within(toolbar).getByRole('button', { name: 'Copy path' })).toHaveFocus()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    await userEvent.click(width === 390 ? screen.getByRole('button', { name: /← Chat/ }) : screen.getByRole('tab', { name: /Chat/ }))
    expect(document.querySelector('.agent-composer textarea')).toBe(composer)
    expect(composer).toBeVisible()
    if (width === 390) expect(composer).not.toHaveFocus()
    else expect(composer).toHaveFocus()
    expect(composer.value).toBe('Draft kept while reading')
    expect([composer.selectionStart, composer.selectionEnd]).toEqual([6, 10])
  })
}

for (const [width, height] of [[390, 844], [1024, 900], [3840, 2160]]) {
  it(`lays out code, Markdown, artifact, diff and commit toolbars at ${width}`, async () => {
    await page.viewport(width, height)
    const views = [
      { kind: 'workspace' as const, path: 'main.go' },
      { kind: 'workspace' as const, path: 'notes.md' },
      { kind: 'artifact' as const, artifact: { id: 'report', name: 'report.md', size: 42, view: 'markdown', content_type: 'text/markdown' } },
      { kind: 'artifact' as const, artifact: { id: 'patch', name: 'changes.diff', size: 100, view: 'diff', content_type: 'text/plain' } },
      { kind: 'commit' as const, sha: 'abcdef1234567890' },
    ]
    const tabs = views.map(view => ({ id: viewId(view), view, preview: false }))
    localStorage.setItem(sessionTabsKey('person-a', 'sess-a'), JSON.stringify({ tabs, active: tabs[0].id }))
    mount(width)
    for (const [index, label, ready] of [[0, 'main.go', '[data-testid="workspace-code"]'], [1, 'notes.md', '.artifact-markdown'], [2, 'report.md', '.artifact-markdown'], [3, 'changes.diff', '[data-testid="diff-view"]'], [4, 'abcdef12', '[data-testid="diff-view"]']] as const) {
      if (index > 0) {
        if (width === 390) {
          screen.getByRole('button', { name: '5 open files' }).focus()
          await userEvent.keyboard('{Enter}')
          const choice = await screen.findByRole('menuitem', { name: label })
          choice.focus()
          await userEvent.keyboard('{Enter}')
        } else await userEvent.click(screen.getByRole('tab', { name: new RegExp(label) }))
      }
      const region = await screen.findByRole('region', { name: index === 4 ? 'Commit abcdef1' : label })
      await waitFor(() => expect(region.querySelector(ready)).not.toBeNull())
      const toolbar = within(region).getByRole('toolbar', { name: 'File actions' })
      expect(toolbar.getBoundingClientRect().height).toBe(38)
      expect(region.querySelector('.artifact-body')!.getBoundingClientRect().top).toBeCloseTo(toolbar.getBoundingClientRect().bottom, 0)
      expect(within(region).queryByRole('button', { name: 'Close' })).toBeNull()
      expect(within(region).queryByRole('button', { name: 'Keep open' })).toBeNull()
      expect(within(region).queryByRole('button', { name: 'Maximize' })).toBeNull()
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
      for (const button of within(toolbar).getAllByRole('button')) {
        expect(button).toHaveAccessibleName()
        const box = button.getBoundingClientRect(), row = toolbar.getBoundingClientRect()
        expect(box.left).toBeGreaterThanOrEqual(row.left)
        expect(box.right).toBeLessThanOrEqual(row.right)
      }
      if (index === 3) {
        expect(within(region).queryByRole('tab', { name: 'Conversation' })).toBeNull()
        expect(within(toolbar).getByRole('button', { name: 'Review' })).toBeTruthy()
        expect(region.querySelector('.diff-main')).toBeVisible()
      }
      if (fileShotDir) await page.screenshot({ path: `${fileShotDir}/${label}-${width}.png` })
      if (index === 1 || index === 2) {
        await userEvent.click(within(toolbar).getByRole('button', { name: 'Raw' }))
        await within(region).findByTestId('workspace-code')
        expect(within(toolbar).getByRole('button', { name: 'Rendered' })).toBeTruthy()
        if (fileShotDir) await page.screenshot({ path: `${fileShotDir}/${label}-raw-${width}.png` })
        await userEvent.click(within(toolbar).getByRole('button', { name: 'Rendered' }))
        await waitFor(() => expect(region.querySelector('.artifact-markdown')).not.toBeNull())
      }
    }
  })
}

it.each([390, 3840])('shows a shell as a regular session tab in the complete workspace at %i', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  mount(width)
  await userEvent.click(await screen.findByRole('button', { name: 'New shell' }))
  await drawShell()
  const input = await waitFor(() => {
    const input = document.querySelector('.shell-terminal textarea')
    expect(input).toBeTruthy(); return input!
  })
  if (width === 390) expect(input).not.toHaveFocus()
  else await waitFor(() => expect(input).toHaveFocus())
  const tab = screen.getByRole('tab', { name: 'Shell' })
  expect(tab).toHaveAttribute('aria-selected', 'true')
  expect(tab.title).toContain('sandbox')
  expect(document.querySelector('.shell-terminal')!.getBoundingClientRect().height).toBeGreaterThan(500)
  const shots = (import.meta.env as Record<string, string | undefined>).VITE_SHELL_TAB_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/shell-tab-workspace-${width}.png` })
  await userEvent.click(screen.getByRole('button', { name: 'Close Shell' }))
  await waitFor(() => expect(document.querySelector('.agent-composer textarea')).toBeVisible())
})

const ansiNames = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white', 'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite']
const lightAnsiSample = '\x1b[2J\x1b[HANSI palette: all 16 colors\r\nWhite samples use ANSI black.\r\n\r\n' + ansiNames.map((name, index) => {
  const code = index < 8 ? 30 + index : 90 + index - 8
  return `[${code}] ${name.padEnd(13)} \x1b[${code}${index === 7 || index === 15 ? ';40' : ''}mANSI sample\x1b[0m\r\n`
}).join('') + '\r\n/workspace/demo-app $ '

it.each([390, 3840].flatMap(width => ['single', 'split', 'light'].map(mode => ({ width, mode }))))('L5: renders $mode shells in the full workspace at $width', async ({ width, mode }) => {
  // Establish a real standard split on desktop. The phone retains that layout,
  // showing one active shell at a time through its ordinary tab switcher.
  const initialWidth = mode === 'split' ? 3840 : width
  await page.viewport(initialWidth, initialWidth === 390 ? 844 : 2160)
  document.documentElement.dataset.theme = mode === 'light' ? 'light' : 'dark'
  await userEvent.click(document.body)
  const view = mount(initialWidth)
  await userEvent.click(await screen.findByRole('button', { name: 'New shell' }))
  await drawShell(mode === 'light' ? lightAnsiSample : undefined)
  if (mode === 'split') {
    await userEvent.click(screen.getByRole('button', { name: 'New shell' }))
    await drawShell()
    const tab = screen.getByRole('tab', { name: 'Shell 2' }).parentElement!
    const transfer = new DataTransfer()
    act(() => { tab.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer: transfer })) })
    expect(transfer.getData(sessionTabDragType)).toContain('shell:')
    const group = document.querySelector<HTMLElement>('[data-group-id]')!, bounds = group.getBoundingClientRect()
    const init = { bubbles: true, cancelable: true, dataTransfer: transfer, clientX: bounds.right - 4, clientY: bounds.top + bounds.height / 2 }
    act(() => { group.dispatchEvent(new DragEvent('dragover', init)); group.dispatchEvent(new DragEvent('drop', init)) })
    await waitFor(() => expect(document.querySelectorAll('[data-group-id]')).toHaveLength(2))
    expect(document.querySelectorAll('.shell-terminal')).toHaveLength(2)
    if (width === 390) {
      const { FakeShell } = await import('../test/shellMock')
      const { restoreShells } = await import('../shellTransport')
      const restored = [...document.querySelectorAll<HTMLElement>('[data-shell-id]')].map(node => new FakeShell(node.dataset.shellId!))
      view.unmount()
      await page.viewport(390, 844)
      vi.mocked(restoreShells).mockResolvedValueOnce(restored)
      mount(390)
      await waitFor(() => expect(restored.every(shell => !!shell.output)).toBe(true))
      act(() => restored.forEach(shell => shell.emit('/workspace/demo-app $ git diff --stat\r\n src/app.ts | 2 +-\r\n 1 file changed, 1 insertion(+), 1 deletion(-)\r\n/workspace/demo-app $ ')))
      expect(document.querySelector('.shell-terminal textarea')).not.toHaveFocus()
    }
  }
  await waitFor(() => expect(screen.getByRole('tab', { name: mode === 'split' ? 'Shell 2' : 'Shell' })).toHaveAttribute('aria-selected', 'true'))
  const renderer = [...document.querySelectorAll<HTMLElement>('.shell-terminal-renderer')].find(node => node.getClientRects().length)!
  await waitFor(() => expect(renderer.getBoundingClientRect().height).toBeGreaterThan(400))
  expect(getComputedStyle(renderer.querySelector('.xterm-scrollable-element')!).backgroundColor).toBe(mode === 'light' ? 'rgb(238, 241, 246)' : 'rgb(13, 17, 23)')
  await waitFor(() => expect(renderer.querySelector('.xterm-screen')).toHaveTextContent(mode === 'light' ? 'ANSI palette: all 16 colors' : 'git diff --stat'))
  if (mode === 'light') for (const name of ansiNames) expect(renderer.querySelector('.xterm-screen')).toHaveTextContent(name)
  const shots = import.meta.env.VITE_SHELL_TAB_SHOTS_DIR
  if (shots) {
    await page.screenshot({ path: `${shots}/shell-${mode}-${width}.png` })
    if (mode === 'light') await page.screenshot({ path: `${shots}/shell-ansi-light-${width}.png` })
  }
}, 30000)
it('L1: returning from a phone shell never raises the composer keyboard', async () => {
  await page.viewport(390, 844); await userEvent.click(document.body); mount(390)
  await userEvent.click(await screen.findByRole('button', { name: 'New shell' }))
  await screen.findByRole('tab', { name: 'Shell' })
  await userEvent.click(screen.getByRole('button', { name: /← Chat/ }))
  const composer = document.querySelector<HTMLTextAreaElement>('.agent-composer textarea')!
  await waitFor(() => expect(composer).toBeVisible())
  expect(composer).not.toHaveFocus()
})

it('keeps phone keyboard tab navigation in the visible scrolling strip', async () => {
  await page.viewport(390, 844); mount(390)
  await openFile('one.ts', true); await openFile('two.ts', true)
  const one = screen.getByRole('tab', { name: /one.ts/ }), two = screen.getByRole('tab', { name: /two.ts/ })
  two.focus(); await userEvent.keyboard('{ArrowLeft}')
  expect(one).toHaveAttribute('aria-selected', 'true'); expect(one).toHaveFocus()
  const viewport = one.closest('.session-tabs')!.getBoundingClientRect(), tab = one.closest('.session-tab')!.getBoundingClientRect()
  expect(tab.left).toBeGreaterThanOrEqual(viewport.left - 1); expect(tab.right).toBeLessThanOrEqual(viewport.right + 1)
  await userEvent.keyboard('{ArrowRight}')
  expect(two).toHaveAttribute('aria-selected', 'true'); expect(two).toHaveFocus()
})

it.each([390, 1024, 3840])('navigates twenty tabs in the session workspace and focus mode at %i', async width => {
  await page.viewport(width, width === 390 ? 844 : 1000)
  const paths = Array.from({ length: 20 }, (_, i) => `module-${String(i).padStart(2, '0')}-with-a-descriptive-name-for-tab-overflow.ts`)
  localStorage.setItem(sessionTabsKey('person-a', 'sess-a'), JSON.stringify({ tabs: paths.map((path, i) => ({ id: `workspace:${path}`, view: { kind: 'workspace', path }, preview: i === 19 })), active: 'chat' }))
  mount(width)
  await userEvent.click(await screen.findByRole('button', { name: 'All tabs' }))
  const menu = screen.getByRole('menu', { name: 'All tabs' })
  expect(within(menu).getAllByRole('menuitemradio')).toHaveLength(21)
  await userEvent.click(within(menu).getByRole('menuitemradio', { name: new RegExp(`${paths[19]}.*Preview`) }))
  await screen.findByRole('region', { name: paths[19] })
  const visible = () => {
    const tab = screen.getByRole('tab', { name: paths[19] }).closest('.session-tab')!, strip = tab.closest('.session-tabs')!
    expect(tab.getBoundingClientRect().left).toBeGreaterThanOrEqual(strip.getBoundingClientRect().left - 1)
    expect(tab.getBoundingClientRect().right).toBeLessThanOrEqual(strip.getBoundingClientRect().right + 1)
  }
  await waitFor(visible)
  const shots = import.meta.env.VITE_TAB_FOCUS_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/tab-overflow-workspace-${width}.png` })
  await userEvent.keyboard('{Control>}{Shift>}{Enter}{/Shift}{/Control}')
  expect(screen.getByRole('button', { name: 'Exit tab focus' })).toBeVisible()
  await waitFor(visible)
  if (shots) await page.screenshot({ path: `${shots}/tab-overflow-focus-${width}.png` })
}, 30000)
