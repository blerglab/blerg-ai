// A static preview of the Plan tab's ticket rows (their status chips) and a checklist item with
// commits and a diff, as a body fragment for a page that adds the built stylesheets (the dist CSS,
// in bundle order). Skipped unless TD_PREVIEW=1:
//   TD_PREVIEW=1 npx vitest run --project unit SessionRail.ticket.preview
// Writes /tmp/td-frag-plan.html.
import { describe, it } from 'vitest'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { RAIL_TAB_KEY } from '../lib/sessionRail'
import { diffFiles, filesOf, makeLive } from '../test/planTabFixture'

interface NodeFs { writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const row = (key: string, title: string, status: string, source = 'jira') => ({ source, key, title, status, url: '', eventId: key, updatedAt: null })
const tickets = [
  row('PRJ-142', 'Saving a large document is slow and can time out', 'In Progress'),
  row('ENG-88', 'Upgrade the storage client', 'Blocked', 'linear'),
  row('PRJ-120', 'Retry a failed upload', 'Done'),
  row('PRJ-131', 'Triage the export reports', 'To Do'),
  row('PRJ-99', 'Old autosave prototype', "Won't Do"),
]
const diff = 'd'.repeat(32)
const items = [
  { text: 'Retry the save', done: true, commits: ['4b1d9c2e'.repeat(5), '9f8e7d6c'.repeat(5)], diff },
  { text: 'Split the storage writes', done: true, commits: ['1a2b3c4d', '2b3c4d5e', '3c4d5e6f', '4d5e6f70', '5e6f7081'] },
  { text: 'Write the release note', done: false },
]

describe('SessionRail ticket preview', () => {
  it.skipIf(!env.TD_PREVIEW)('writes /tmp/td-frag-plan.html', async () => {
    const { writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    localStorage.setItem(RAIL_TAB_KEY, 'plan')
    render(
      <MemoryRouter>
        <SessionRail
          id="rail" session={makeSession({ title: 'Ship it', status: 'running' })} mode="panel" open toolChanges={0} onClose={() => {}}
          live={makeLive({ tickets, checklist: { items, eventId: 'c1', updatedAt: null }, files: filesOf(diffFiles) })}
          onOpenArtifact={() => {}}
        />
      </MemoryRouter>,
    )
    fireEvent.click(screen.getByRole('tab', { name: /Plan/ }))
    await act(async () => { await new Promise(r => setTimeout(r, 20)) })
    // The counts come from the diff itself, read through the page's transport: none in a test.
    writeFileSync('/tmp/td-frag-plan.html', document.body.innerHTML)
    cleanup()
  }, 60_000)
})
