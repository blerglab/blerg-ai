// A static preview of the session page with its side panel, for a person to look at in a browser:
// renders the page at desktop width with the panel open beside the chat, and at phone width with
// the drawer open, on its Live, History, Files and Capabilities tabs, and writes the markup with the app's
// stylesheets to ~/rail-preview.html (?phone shows the phone, ?phone&closed without the drawer,
// ?history the History tab with the open topic and the unmarked line open, ?files the Files tab with
// a file's earlier versions open (?phone&files too), ?capabilities (or ?skills) the Capabilities tab, ?background the session
// idle with its background work running, ?light the light theme).
// Skipped unless SI_PREVIEW=1:  SI_PREVIEW=1 npx vitest run --project unit SessionRail.preview
import { describe, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { useTranscriptStore } from '@blerglab/chat'
import SessionDetail from './SessionDetail'
import { useSessionStore } from '../hooks/useSessionStore'
import { seedRichRail } from '../test/railFixture'
import { reelReply } from '../test/reelFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import type { LiveReply } from '../lib/liveState'
import type { AgentEvent } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))
// The chat's transport reads the files with this token, taken as fresh.
vi.mock('../authClient', async (orig) => ({ ...(await orig<typeof import('../authClient')>()), getAccessToken: () => 'tok' }))
vi.mock('../claims', async (orig) => ({ ...(await orig<typeof import('../claims')>()), tokenExpired: () => false }))

// The app's tsconfig has no Node types: the file system comes in untyped, through a dynamic import.
interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

function viewport(width: number) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => {
    const m = /max-width:\s*(\d+)px/.exec(query)
    return {
      matches: m ? width <= Number(m[1]) : false, media: query, onchange: null,
      addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }
  }) as unknown as typeof window.matchMedia
}

/** The History the preview shows: two marked topics (an open one, a finished one whose plan.md is
 *  capped: 25 versions, 3 listed) and the unmarked work (two files, five subagents). */
function previewReel() {
  const reply = reelReply()
  const plan = reply.topics[1].items[1]
  if (plan.type === 'document') plan.versions_total = 25
  return reply
}

/** The Files the preview shows: a plan in three versions, a CSV, a screenshot you attached. */
const previewFiles = [
  { id: 'f3', name: 'plan.md', size: 6144, content_type: 'text/markdown', view: 'markdown', version: 3, latest_version: 3, created_at: '2026-10-15T11:20:00Z' },
  { id: 'f4', name: 'screenshot.png', size: 284000, content_type: 'image/png', view: 'image', origin: 'user', version: 1, latest_version: 1, created_at: '2026-10-15T11:05:00Z' },
  { id: 'f2', name: 'plan.md', size: 5120, content_type: 'text/markdown', view: 'markdown', version: 2, latest_version: 3, created_at: '2026-10-15T10:40:00Z' },
  { id: 'f5', name: 'coverage.csv', size: 18432, content_type: 'text/csv', view: 'csv', version: 1, latest_version: 1, created_at: '2026-10-15T10:30:00Z' },
  { id: 'f1', name: 'plan.md', size: 4096, content_type: 'text/markdown', view: 'markdown', version: 1, latest_version: 3, created_at: '2026-10-15T10:00:00Z' },
]

/** What the session loaded, as the Capabilities tab shows it. */
const previewCaps = {
  seq: 40, ts: '2026-10-15T10:00:00Z',
  capabilities: {
    engine: 'claude', engine_name: 'Claude Code', version: '2.1.283', model: 'claude-opus-5-5', permission_mode: 'bypassPermissions',
    note: 'What Claude Code loaded when this session\'s latest turn started.',
    groups: [
      { id: 'skills', label: 'Skills', items: [
        { name: 'release-notes', description: 'Writes release notes from the merged pull requests since the last tag.', source: 'user' },
        { name: 'brainstorming', description: 'Explores intent, requirements and design before implementation.', source: 'plugin' },
        { name: 'test-driven-development', description: 'Write the test first, watch it fail, write minimal code to pass.', source: 'plugin' },
      ] },
      { id: 'plugins', label: 'Plugins', items: [{ name: 'superpowers', detail: 'v5.0.2', status: 'enabled' }] },
      { id: 'mcp', label: 'MCP servers', items: [
        { name: 'issues', status: 'connected', detail: '3 tools', source: 'user' },
        { name: 'drive', status: 'needs-auth' },
      ] },
      { id: 'commands', label: 'Slash commands', items: Array.from({ length: 18 }, (_, i) => ({ name: `/command-${i + 1}` })) },
      { id: 'tools', label: 'Tools', total: 42, items: ['Bash', 'Read', 'Edit', 'Write', 'Grep', 'Glob'].map(name => ({ name })) },
    ],
  },
}

type View = 'live' | 'history' | 'files' | 'capabilities' | 'background'

// The live state the server answers for the session being drawn.
let liveNow: LiveReply | null = null

async function snapshot(width: number, view: View = 'live'): Promise<string> {
  const history = view === 'history'
  const background = view === 'background'
  viewport(width)
  localStorage.setItem(RAIL_TAB_KEY, background ? 'live' : view)
  const { failed, live } = seedRichRail()
  liveNow = live
  if (background) {
    // Its turn is over (the foreground subagent with it), its background work is not: the header
    // and the Session facts say "working in background", not idle.
    liveNow = { ...live, subagents: live.subagents.filter(x => x.background) }
    useSessionStore.setState(st => ({ sessions: st.sessions.map(x => ({ ...x, status: 'idle' as const })) }))
  }
  render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
    </MemoryRouter>,
  )
  // The live state arrives, then a task fails (after the server's reply: the panel folds it in, and
  // the server's count drops with it).
  await waitFor(() => { if (!screen.getByTestId('rail-badge')) throw new Error('not yet') })
  act(() => {
    useTranscriptStore.getState().ingest(failed)
    // What the session loaded: the Capabilities tab's badge counts what is enabled in it.
    useTranscriptStore.getState().ingest({ session_id: 'sess-a', client_event_id: 'caps1', seq: 200, ts: new Date().toISOString(), kind: 'capabilities', payload: previewCaps.capabilities } as AgentEvent)
    useSessionStore.setState(st => ({ sessions: st.sessions.map(x => ({ ...x, background_tasks: 5 })) }))
  })
  if (width < 1024) fireEvent.click(screen.getByTestId('rail-toggle'))
  if (history) {
    // The open topic and the unmarked line, opened; the finished topic stays a card.
    const heads = await screen.findAllByTestId('reel-head')
    fireEvent.click(heads[0])
    fireEvent.click(screen.getByTestId('reel-unmarked-head'))
  } else if (view === 'files') {
    // The plan's earlier versions, opened.
    fireEvent.click(await screen.findByRole('button', { name: /Earlier versions/ }))
  } else if (view === 'capabilities') {
    await screen.findByText('release-notes')
  } else {
    // The tool change count arrives with the live state.
    await waitFor(() => { if (!screen.getByTestId('rail-tool-changes')) throw new Error('not yet') })
  }
  const html = Array.from(document.body.children).map((el) => el.outerHTML).join('\n')
  cleanup()
  return html
}

describe('SessionRail preview', () => {
  it.skipIf(!env.SI_PREVIEW)('writes ~/rail-preview.html', async () => {
    const reply = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json', ETag: '"1"' } }))
    vi.stubGlobal('fetch', vi.fn((url: string) => String(url).includes('/topics') ? reply(previewReel())
      : String(url).includes('/live') ? reply(liveNow)
      : String(url).endsWith('/artifacts') ? reply({ artifacts: previewFiles })
      : String(url).includes('/capabilities') ? reply(previewCaps)
      : Promise.resolve({
        ok: true,
        json: () => Promise.resolve(String(url).includes('/api/tool-changes') ? { tool_changes: [{ id: 't1', kind: 'changed', muted: false }] } : {}),
      })))
    localStorage.setItem(RAIL_PREF_KEY, '1')
    const desktop = await snapshot(1280)
    const phone = await snapshot(390)
    const desktopHistory = await snapshot(1280, 'history')
    const phoneHistory = await snapshot(390, 'history')
    const desktopFiles = await snapshot(1280, 'files')
    const phoneFiles = await snapshot(390, 'files')
    const desktopCapabilities = await snapshot(1280, 'capabilities')
    const desktopBackground = await snapshot(1280, 'background')
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = [
      '../index.css', './CapabilitiesPanel.css', './AgentChatView.css', './SessionRail.css',
      '../../../../packages/chat/src/theme/blerg.css', '../../../../packages/chat/src/components/chat.css',
      '../../../../packages/chat/src/components/markdown.css',
    ].map((f) => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
    writeFileSync(`${env.HOME}/rail-preview.html`, `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Session panel preview</title>
<style>${css}
body { margin: 0; height: 100vh; background: var(--basalt); color: var(--chalk); font-family: var(--body); }
#desktop > div, #phone > div, #desktop-history > div, #phone-history > div, #desktop-files > div, #phone-files > div,
#desktop-capabilities > div, #desktop-background > div { height: 100vh; }
</style>
</head><body>
<div id="desktop">${desktop}</div>
<div id="phone">${phone}</div>
<div id="desktop-history">${desktopHistory}</div>
<div id="phone-history">${phoneHistory}</div>
<div id="desktop-files">${desktopFiles}</div>
<div id="phone-files">${phoneFiles}</div>
<div id="desktop-capabilities">${desktopCapabilities}</div>
<div id="desktop-background">${desktopBackground}</div>
<script>
  /* ?phone shows the phone layout (?phone&closed without the drawer), ?history the History tab,
     ?files the Files tab (?phone&files too), ?capabilities (or ?skills) the Capabilities tab, ?background the session idle
     with background work running, ?light the light theme */
  const q = location.search
  const tab = ['history', 'files', 'capabilities', 'skills'].find((t) => q.includes(t))?.replace('skills', 'capabilities')
  const keep = q.includes('background') ? 'desktop-background'
    : (q.includes('phone') ? 'phone' : 'desktop') + (tab ? '-' + tab : '')
  for (const id of ['desktop', 'phone', 'desktop-history', 'phone-history', 'desktop-files', 'phone-files', 'desktop-capabilities', 'desktop-background']) {
    const el = document.getElementById(id)
    if (id !== keep && el) el.remove()
  }
  if (location.search.includes('closed')) document.querySelectorAll('.rail-overlay').forEach((n) => n.remove())
  if (location.search.includes('light')) document.documentElement.dataset.theme = 'light'
</script>
</body></html>
`)
  }, 60_000)
})
