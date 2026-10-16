// A static preview of the Session insights dialog for a person to look at in a browser: renders the
// dialog with a rich report and writes the markup with the app's stylesheets to ~/si-preview.html.
// Skipped unless SI_PREVIEW=1:  SI_PREVIEW=1 npx vitest run --project unit SessionInsights.preview
import { describe, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import SessionInsights from './SessionInsights'
import type { SessionInsightsResponse } from '../lib/insights'

// The app's tsconfig has no Node types: the file system comes in untyped, through a dynamic import.
interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const tok = (input: number, output: number, cache_read: number, cache_write: number, cost_usd: number | null, turns = 0) =>
  ({ turns, input, output, cache_read, cache_write, cost_usd })

const report: SessionInsightsResponse = {
  session_id: 'sess-preview', title: 'Refactor the session store so a daemon restart keeps every agent session alive',
  kind: 'agent', status: 'running', runtime: 'cluster', engine: 'claude', model: 'claude-opus-5-5',
  started_at: '2026-10-14T09:00:00Z', ended_at: null, generated_at: '2026-10-14T12:12:00Z',
  lifetime_seconds: 11_520, running_seconds: 5_400, waiting_seconds: 900, idle_seconds: 5_220,
  turns: 64, tool_calls: 412,
  tool_calls_by_tool: [
    { key: 'Bash', count: 163 }, { key: 'Read', count: 97 }, { key: 'Edit', count: 71 }, { key: 'Grep', count: 38 },
    { key: 'mcp__homelab__postgres_query_readonly', count: 21 }, { key: 'Write', count: 12 }, { key: 'Glob', count: 10 },
  ],
  messages_from_person: 9,
  subagents: {
    count: 4,
    by_type: [{ key: 'general-purpose', count: 2 }, { key: 'Explore', count: 1 }, { key: 'code-reviewer', count: 1 }],
    by_model: [{ key: 'claude-sonnet-5', count: 3 }, { key: 'claude-haiku-5', count: 1 }],
    tool_calls: 133,
  },
  tokens: {
    totals: tok(48_200, 310_000, 18_400_000, 1_120_000, 31.84, 64),
    main: tok(41_000, 262_000, 15_900_000, 940_000, 29.1, 64),
    subagents: tok(7_200, 48_000, 2_500_000, 180_000, 2.74),
    by_model: [
      { model: 'claude-sonnet-5', priced: true, ...tok(6_000, 40_000, 2_200_000, 150_000, 2.74) },
      { model: 'claude-opus-5-5', priced: true, ...tok(41_000, 262_000, 15_900_000, 940_000, 29.1, 64) },
      { model: 'claude-haiku-5', priced: false, ...tok(1_200, 8_000, 300_000, 30_000, null) },
    ],
    cache_hit_rate: 0.94,
    unpriced_models: ['claude-haiku-5'],
  },
  artifacts: 3,
  proposals: { pending: 1, approved: 4, declined: 1, expired: 0 },
  mcp_calls: { total: 27, by_outcome: [{ key: 'ok', count: 24 }, { key: 'refused', count: 2 }, { key: 'error', count: 1 }] },
}

describe('SessionInsights preview', () => {
  it.skipIf(!env.SI_PREVIEW)('writes ~/si-preview.html', async () => {
    apiFetch.mockImplementation(() => Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(report) } as Response))
    render(<SessionInsights sessionId="sess-preview" title={report.title} live onClose={() => {}} />)
    await screen.findByTestId('si-time')
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = ['../index.css', './CapabilitiesPanel.css', './SessionInsights.css']
      .map((f) => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
    const markup = Array.from(document.body.children).map((el) => el.outerHTML).join('\n')
    writeFileSync(`${env.HOME}/si-preview.html`, `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><title>Session insights preview</title>
<style>${css}
body { margin: 0; min-height: 100vh; background: var(--basalt); color: var(--chalk); font-family: var(--body); }
</style>
<script>/* ?light previews the light theme */ if (location.search.includes('light')) document.documentElement.dataset.theme = 'light'</script>
</head><body>${markup}</body></html>
`)
  })
})
