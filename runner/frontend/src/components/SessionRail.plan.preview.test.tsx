// A static preview of the session panel's Live tab (with its summary line) and its Plan tab (a ticket,
// a long checklist, two diffs), side by side, for a person to look at in a browser. Skipped unless SI_PREVIEW=1:
//   SI_PREVIEW=1 npx vitest run --project unit SessionRail.plan.preview
// Writes /tmp/pt-desktop.html and /tmp/pt-phone.html (the same two views, stacked for a phone).
import { describe, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { diffFiles, filesOf, makeLive, planItems } from '../test/planTabFixture'

interface NodeFs { readFileSync(p: URL, enc: 'utf8'): string; writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const ticket = { source: 'tracker', key: 'PRJ-12', title: 'Fix the slow save on large documents', status: 'In progress', url: '', eventId: 'e1', updatedAt: null }

function snapshot(tab: 'live' | 'plan'): string {
  localStorage.setItem(RAIL_TAB_KEY, tab)
  render(
    <MemoryRouter>
      <SessionRail
        id="rail" session={makeSession({ title: 'Ship it', status: 'running' })} mode="panel" open toolChanges={0} onClose={() => {}}
        live={makeLive({ tickets: [ticket], checklist: { items: planItems, eventId: 'c1', updatedAt: null }, files: filesOf(diffFiles) })}
        onOpenArtifact={() => {}}
      />
    </MemoryRouter>,
  )
  if (tab === 'plan') fireEvent.click(screen.getByRole('tab', { name: /Plan/ }))
  const html = Array.from(document.body.children).map(el => el.outerHTML).join('\n')
  cleanup()
  return html
}

describe('SessionRail Plan preview', () => {
  it.skipIf(!env.SI_PREVIEW)('writes /tmp/pt-*.html', async () => {
    const { readFileSync, writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    const css = [
      '../index.css', './SessionRail.css', '../../../../packages/chat/src/theme/blerg.css',
      '../../../../packages/chat/src/components/chat.css', '../../../../packages/chat/src/components/markdown.css',
    ].map(f => readFileSync(new URL(f, import.meta.url), 'utf8')).join('\n')
    const live = snapshot('live')
    const plan = snapshot('plan')
    const page = (flow: string) => `<!doctype html><html lang="en" data-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>${css}
body { margin: 0; background: var(--basalt); color: var(--chalk); font-family: var(--body); }
.col { ${flow} } .col > div, .col > aside { height: 100%; width: 100%; }
</style></head><body>${flow.includes('row') ? '<div style="display:flex;height:100vh">' : '<div>'}<div class="col">${live}</div><div class="col">${plan}</div></div></body></html>`
    writeFileSync('/tmp/pt-desktop.html', page('width: 420px; height: 100vh; border-right: 1px solid var(--scree-2); row: 1;'))
    writeFileSync('/tmp/pt-phone.html', page('width: 390px; height: 900px;'))
  }, 60_000)
})
