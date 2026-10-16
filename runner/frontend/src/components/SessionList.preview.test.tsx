// A static preview of the sessions sidebar with usage-limit meters and compact cards, for a person to
// look at in a browser: the sidebar at desktop width and the phone's list, written with the app's
// stylesheets to ~/limits-preview.html (?phone shows the phone, ?light the light theme).
// Skipped unless SI_PREVIEW=1:  SI_PREVIEW=1 npx vitest run --project unit SessionList.preview
import { describe, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionList from './SessionList'
import { useSessionStore } from '../hooks/useSessionStore'
import { makeDaemon, makeSession } from '../test/fixtures'
import type { SessionInfo } from '../types'

vi.mock('../ws', () => import('../test/wsMock'))

interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const minutesFromNow = (m: number) => new Date(Date.now() + m * 60_000).toISOString()
const minutesAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString()

function seed() {
  const desk = makeDaemon({
    id: 'd1', name: 'desk', version: 'dev',
    usage_limits: [
      { provider: 'codex', windows: [
        { id: '5h', used_percent: 42, resets_at: minutesFromNow(130) },
        { id: '7d', used_percent: 78, resets_at: minutesFromNow(3 * 1440 + 240) },
      ] },
      { provider: 'claude', windows: [{ id: 'capped', used_percent: 100, resets_at: minutesFromNow(95) }] },
    ],
  })
  const s = (over: Partial<SessionInfo>) => makeSession({ daemon_id: 'd1', runtime: 'daemon', kind: 'agent', ...over })
  const sessions: SessionInfo[] = [
    s({ id: 's1', title: 'Usage meters in the sidebar', repo: 'blerglab/blerg', status: 'waiting', unread: true, model: 'claude-opus-5', started_at: minutesAgo(40) }),
    s({ id: 's2', title: 'Fix the board drag handle', repo: 'blerglab/board', status: 'running', model: 'gpt-5.5', engine: 'codex', started_at: minutesAgo(12), background_tasks: 2 }),
    s({ id: 's3', title: 'Release notes for 0.2.0', repo: 'blerglab/blerg', status: 'running', runtime: 'cluster', model: 'claude-sonnet-5', started_at: minutesAgo(30) }),
    s({ id: 's4', title: 'Widget pricing page', repo: 'acme/widget-storefront-with-a-long-name', status: 'idle', runtime: 'docker', model: 'claude-sonnet-5', started_at: minutesAgo(90) }),
    s({ id: 's5', title: 'Scratch: try the new API', repo: '.scratch-granite-8000', status: 'idle', kind: 'tmux', started_at: minutesAgo(200) }),
    s({ id: 's6', title: 'Infra DNS cleanup', repo: 'acme/infra', status: 'running', starred: true, model: 'claude-opus-5', started_at: minutesAgo(5) }),
    s({ id: 's7', title: 'Flaky e2e on the launch sheet', repo: 'blerglab/blerg', status: 'idle', model: 'gpt-5.5', engine: 'codex', started_at: minutesAgo(300), background_tasks: 2 }),
    s({ id: 's8', title: 'Dependency bumps', repo: 'blerglab/board', status: 'stopped', started_at: minutesAgo(800), ended_at: minutesAgo(700) }),
  ]
  useSessionStore.setState({
    daemons: [desk], sessions,
    statusChangedAt: Object.fromEntries(sessions.map(x => [x.id, Date.now()])),
    serverVersion: 'dev', serverRelease: '0.2.0', serverCommit: 'c58ac8a',
  })
}

async function snapshot(variant: 'sidebar' | 'page'): Promise<string> {
  seed()
  render(<MemoryRouter initialEntries={['/sessions/s2']}><SessionList variant={variant} /></MemoryRouter>)
  await screen.findByTestId('focus-pill-all')
  const html = Array.from(document.body.children).map((el) => el.outerHTML).join('\n')
  cleanup()
  return html
}

describe('SessionList preview', () => {
  it.skipIf(!env.SI_PREVIEW)('writes ~/limits-preview.html', async () => {
    vi.stubGlobal('fetch', vi.fn((url: string) => Promise.resolve({
      ok: true,
      json: () => Promise.resolve(String(url).includes('/api/cluster/status') ? { configured: true, max_sessions: 5, active_sessions: 1 } : {}),
    })))
    const desktop = await snapshot('sidebar')
    const phone = await snapshot('page')
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = ['../index.css', '../App.css', './UsageMeters.css']
      .map((f) => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
    writeFileSync(`${env.HOME}/limits-preview.html`, `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Sidebar preview</title>
<style>${css}
body { margin: 0; height: 100vh; background: var(--basalt); color: var(--chalk); font-family: var(--body); }
#desktop { display: flex; height: 100vh; }
#desktop .main { flex: 1; background: var(--scree); }
</style>
</head><body>
<div id="desktop">${desktop}<div class="main"></div></div>
<div id="phone">${phone}</div>
<script>
  const keep = location.search.includes('phone') ? 'phone' : 'desktop'
  for (const id of ['desktop', 'phone']) if (id !== keep) document.getElementById(id).remove()
  if (location.search.includes('light')) document.documentElement.dataset.theme = 'light'
</script>
</body></html>
`)
  })
})
