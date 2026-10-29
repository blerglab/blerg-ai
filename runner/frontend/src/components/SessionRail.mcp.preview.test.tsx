// A static preview of the session page with the MCP servers section, the held first message and a
// checklist with commit chips, written with the app's stylesheets to /tmp/mu-preview.html (desktop
// and phone). Skipped unless SI_PREVIEW=1:  SI_PREVIEW=1 npx vitest run --project unit SessionRail.mcp.preview
import { describe, it, vi } from 'vitest'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import SessionDetail from './SessionDetail'
import { seedRichRail } from '../test/railFixture'
import { RAIL_PREF_KEY, RAIL_TAB_KEY } from '../lib/sessionRail'
import type { LiveReply } from '../lib/liveState'

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


const MIN = 60_000
let liveNow: LiveReply | null = null

async function snapshot(width: number, tab: 'live' | 'plan' | 'capabilities' = 'live'): Promise<string> {
  viewport(width)
  localStorage.setItem(RAIL_TAB_KEY, tab)
  const { live } = seedRichRail()
  const at = (ms: number) => new Date(Date.now() + ms).toISOString()
  liveNow = {
    ...live,
    checklist: {
      items: [
        { text: 'Reproduce the restart bug', done: true, commits: ['9f2c1ab', '3d7e0c4'] },
        { text: 'Persist the session store before exit', done: true, commits: ['a41b9e2'] },
        { text: 'Resume every agent session on start', done: false },
      ],
      event_id: 'ck1', updated_at: at(-MIN), commit_urls: { a41b9e2: 'https://forge.example.test/acme/app/commit/a41b9e2' },
    },
    tickets: [{ source: 'tracker', key: 'PRJ-12', title: 'Resume sessions after a restart', status: 'In progress', url: '', event_id: 'e1', seq: 1, updated_at: '2026-10-17T12:00:00Z' }],
    mcp: {
      servers: [
        { name: 'issues', status: 'connected', source: 'user', tools: 12 },
        { name: 'drive', status: 'needs-auth', source: 'claude.ai', tools: 0 },
        { name: 'docs', status: 'pending', source: 'project', tools: 0 },
      ],
      settled: false, since: at(-12_000), engine: 'claude',
    },
    mcp_hold: { since: at(-12_000), until: at(18_000) },
    can_release_prompt: true,
  } as LiveReply
  render(
    <MemoryRouter initialEntries={['/sessions/sess-a']}>
      <Routes><Route path="/sessions/:id" element={<SessionDetail />} /></Routes>
    </MemoryRouter>,
  )
  await waitFor(() => { if (!screen.getByTestId('rail-toggle').querySelector('.icon-btn-count')) throw new Error('not yet') })
  if (width < 1024) fireEvent.click(screen.getByTestId('rail-toggle'))
  if (tab === 'live') await waitFor(() => { if (!screen.getByTestId('rail-plan-summary')) throw new Error('not yet') }, { timeout: 4000 })
  if (tab === 'capabilities') await screen.findByText('issues', {}, { timeout: 4000 })
  if (tab === 'plan') {
    const check = await screen.findByTestId('rail-checklist')
    for (const b of within(check).getAllByRole('button')) act(() => { fireEvent.click(b) })
  }
  const html = Array.from(document.body.children).map((el) => el.outerHTML).join('\n')
  cleanup()
  return html
}

describe('Panel summary, plan and capabilities preview', () => {
  it.skipIf(!env.SI_PREVIEW)('writes /tmp/mu-preview.html', async () => {
    const reply = (body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json', ETag: '"1"' } }))
    vi.stubGlobal('fetch', vi.fn((url: string) => String(url).includes('/live') ? reply(liveNow)
      : String(url).includes('/capabilities') ? reply({ seq: 40, ts: '2026-10-15T10:00:00Z', capabilities: { engine: 'claude', groups: [{ id: 'mcp', label: 'MCP servers', items: [{ name: 'issues', status: 'pending', source: 'user' }, { name: 'drive', status: 'pending' }] }] } })
      : String(url).endsWith('/artifacts') ? reply({ artifacts: [] })
      : Promise.resolve({ ok: true, json: () => Promise.resolve(String(url).includes('/api/tool-changes') ? { tool_changes: [] } : {}) })))
    localStorage.setItem(RAIL_PREF_KEY, '1')
    const desktop = await snapshot(1280)
    const phone = await snapshot(390)
    const plan = await snapshot(1280, 'plan')
    const caps = await snapshot(1280, 'capabilities')
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = [
      '../index.css', './CapabilitiesPanel.css', './AgentChatView.css', './SessionRail.css',
      '../../../../packages/chat/src/theme/blerg.css', '../../../../packages/chat/src/components/chat.css',
      '../../../../packages/chat/src/components/markdown.css', '../../../../packages/chat/src/components/worklist.css',
      '../../../../packages/chat/src/components/commitChips.css',
    ].map((f) => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
    writeFileSync('/tmp/mu-preview.html', `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Panel summary, plan and capabilities preview</title>
<style>${css}
body { margin: 0; height: 100vh; background: var(--basalt); color: var(--chalk); font-family: var(--body); }
#desktop > div, #phone > div, #plan > div, #caps > div { height: 100vh; }
</style>
</head><body>
<div id="desktop">${desktop}</div>
<div id="phone">${phone}</div>
<div id="plan">${plan}</div>
<div id="caps">${caps}</div>
<script>
  /* ?phone shows the phone layout, ?light the light theme */
  const want = location.search.includes('phone') ? 'phone' : location.search.includes('plan') ? 'plan' : location.search.includes('caps') ? 'caps' : 'desktop'
  for (const id of ['desktop', 'phone', 'plan', 'caps']) if (id !== want) document.getElementById(id).remove()
  if (location.search.includes('light')) document.documentElement.dataset.theme = 'light'
</script>
</body></html>
`)
  }, 60_000)
})
