// A static preview of the whole Insights page for a person to look at in a browser: renders the page
// with a rich report (every section populated) twice, once as a desktop and once as a phone (the
// narrow layout is chosen by a media query in the page, so the narrow render stubs matchMedia), and
// writes both with the app's stylesheets to ~/insights-page-preview.html. A media query in the file
// shows the phone render at 640 px or less.
// Skipped unless SI_PREVIEW=1:  SI_PREVIEW=1 npx vitest run --project unit Insights.preview
import { afterEach, describe, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import Insights from './Insights'
import { richInsights as report, richPrices as prices } from '../test/insightsFixture'

// The app's tsconfig has no Node types: the file system comes in untyped, through a dynamic import.
interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

function stubNarrow(narrow: boolean) {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: narrow && query.includes('max-width'),
    media: query, onchange: null,
    addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {},
    dispatchEvent: () => false,
  }))
}

async function renderMarkup(narrow: boolean): Promise<string> {
  stubNarrow(narrow)
  render(<MemoryRouter><Insights /></MemoryRouter>)
  // the stored prices arrive after the report: a Remove button means they are on screen
  await screen.findByRole('button', { name: /remove claude-opus-5-5/i })
  const markup = Array.from(document.body.children).map((el) => el.innerHTML).join('\n')
  cleanup()
  return markup
}

describe('Insights page preview', () => {
  afterEach(() => { vi.restoreAllMocks() })

  it.skipIf(!env.SI_PREVIEW)('writes ~/insights-page-preview.html', async () => {
    apiFetch.mockImplementation((url: string) => {
      const body = url.startsWith('/api/insights/prices') ? prices : report
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve(body) } as Response)
    })
    const wide = await renderMarkup(false)
    const narrow = await renderMarkup(true)
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = ['../index.css', './Insights.css'].map((f) => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
    writeFileSync(`${env.HOME}/insights-page-preview.html`, `<!doctype html>
<html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Insights page preview</title>
<style>${css}
body { margin: 0; min-height: 100vh; background: var(--basalt); color: var(--chalk); font-family: var(--body); }
.preview-narrow { display: none; }
@media (max-width: 640px) { .preview-wide { display: none; } .preview-narrow { display: block; } }
</style>
<script>/* ?light previews the light theme */ if (location.search.includes('light')) document.documentElement.dataset.theme = 'light'</script>
</head><body><div class="preview-wide">${wide}</div><div class="preview-narrow">${narrow}</div></body></html>
`)
  })
})
