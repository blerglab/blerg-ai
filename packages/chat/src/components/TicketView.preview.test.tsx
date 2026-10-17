// A static preview of the ticket cards (in progress, done, blocked, re-pinned), a checklist row
// with commits and a diff, and the ticket modal, written as body fragments for a page that adds
// the built stylesheets (the runner's dist CSS, in bundle order). Skipped unless TD_PREVIEW=1:
//   TD_PREVIEW=1 npx vitest run src/components/TicketView.preview
// Writes /tmp/td-frag-chat.html (the transcript), /tmp/td-frag-modal.html (the modal open) and
// /tmp/td-frag-diff.html (the diff opened from a sha chip, at its commit).
import { describe, it } from 'vitest'
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import ChatView from './ChatView'
import { createFakeTransport } from '../test/fakeTransport'
import { SESSION, ev, meta, resetChatState, seed } from '../test/chat'

interface NodeFs { writeFileSync(p: string, data: string): void }
const nodeModule = (name: string) => import(/* @vite-ignore */ name)
const env = import.meta.env as Record<string, string | undefined>

const DIFF = 'd'.repeat(32)
const SHA1 = '4b1d9c2e'.repeat(5)
const SHA2 = '9f8e7d6c'.repeat(5)
const PATCH = [
  `commit ${SHA1}`, 'Author: Dev', '', '    Retry the save', '',
  'diff --git a/app/save.ts b/app/save.ts', '--- a/app/save.ts', '+++ b/app/save.ts', '@@ -1,3 +1,5 @@', ' a', '-b', '+c', '+d', '+e',
  `commit ${SHA2}`, 'Author: Dev', '', '    Test the retry', '',
  'diff --git a/app/save.test.ts b/app/save.test.ts', '--- a/app/save.test.ts', '+++ b/app/save.test.ts', '@@ -1 +1,3 @@', ' a', '+b', '+c', '',
].join('\n')

const description = `Saving a document over 5 MB takes more than ten seconds and sometimes times out.

## Steps to reproduce

1. Open a large document.
2. Change one line and save.

Expected: the save finishes in under a second.`

const main = {
  source: 'jira', key: 'PRJ-142', url: 'https://tracker.example.test/browse/PRJ-142', title: 'Saving a large document is slow and can time out',
  status: 'In Progress', type: 'Task', priority: 'High', assignee: 'Release owner', reporter: 'Support desk', labels: ['performance', 'storage'],
  description,
  fields: [{ name: 'Sprint', value: 'Sprint 42' }, { name: 'Story points', value: '3' }, { name: 'Fix version', value: '0.3.1' }],
  acceptance: ['A 5 MB document saves in under a second', 'A failed save is retried once', 'The retry is covered by a test'],
  comments: [
    { author: 'Support desk', at: '2026-10-15T14:00:00Z', body: 'Three people reported this this week. It happens on **every** large file.' },
    { author: 'Release owner', at: '2026-10-16T09:30:00Z', body: 'Taking it for 0.3.1. The retry goes in `save.ts`.' },
  ],
}

describe('ticket preview', () => {
  it.skipIf(!env.TD_PREVIEW)('writes /tmp/td-frag-*.html', async () => {
    const { writeFileSync } = (await nodeModule('node:fs')) as NodeFs
    resetChatState()
    seed(
      ev({ seq: 1, kind: 'ticket', ts: '2026-10-17T09:00:00Z', payload: { action: 'pin', ticket: main } }),
      ev({ seq: 2, kind: 'checklist', ts: '2026-10-17T09:05:00Z', payload: { op: 'tick', changed: { index: 0, text: 'Retry the save', done: true }, items: [
        { text: 'Retry the save', done: true, commits: [SHA1, SHA2], diff: DIFF },
        { text: 'Write the release note', done: false },
      ] } }),
      ev({ seq: 3, kind: 'ticket', ts: '2026-10-17T09:10:00Z', payload: { action: 'pin', ticket: { source: 'github', key: '311', url: 'https://forge.example.test/issues/311', title: 'Export keeps the last column width', status: 'Done', type: 'Bug', priority: 'Low', assignee: 'Dev', description: 'The CSV export drops the width of the last column.' } } }),
      ev({ seq: 4, kind: 'ticket', ts: '2026-10-17T09:12:00Z', payload: { action: 'pin', ticket: { source: 'linear', key: 'ENG-88', title: 'Upgrade the storage client', status: 'Blocked', type: 'Chore', priority: 'Medium', description: 'Waiting for the vendor to publish 4.2.' } } }),
      ev({ seq: 5, kind: 'ticket', ts: '2026-10-17T09:15:00Z', payload: { action: 'update', ticket: { ...main, key: 'PRJ-150', title: 'Show the save progress', status: 'In Progress', comments: [], acceptance: [] }, previous: { status: 'Open', title: 'Show the save progress' } } }),
      ev({ seq: 6, kind: 'ticket', ts: '2026-10-17T09:16:00Z', payload: { action: 'pin', ticket: { source: 'jira', key: 'PRJ-99', title: 'Old autosave prototype', status: "Won't Do", type: 'Task' } } }),
    )
    const t = createFakeTransport({ files: { items: [
      { id: DIFF, name: 'retry-the-save.diff', size: PATCH.length, content_type: 'text/x-diff', view: 'diff', origin: 'agent' },
    ] } })
    t.files!.serve(PATCH)
    render(<ChatView session={SESSION} transport={t.transport} meta={meta({ status: 'idle' })} />)
    const row = await screen.findByTestId('checklist-row')
    fireEvent.click(within(row).getByRole('button', { name: /Checklist/ }))
    await within(row).findByText('+5')
    await act(async () => { await new Promise(r => setTimeout(r, 50)) })
    writeFileSync('/tmp/td-frag-chat.html', document.body.innerHTML)
    fireEvent.click(within(screen.getAllByTestId('ticket-card')[0]).getByRole('button', { name: 'Open ticket' }))
    await screen.findByRole('dialog')
    writeFileSync('/tmp/td-frag-modal.html', document.body.innerHTML)
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    // A sha chip opens the item's diff at its commit.
    fireEvent.click(within(row).getByRole('button', { name: /Open commit 9f8e7d6/ }))
    await screen.findAllByTestId('diff-commit')
    writeFileSync('/tmp/td-frag-diff.html', document.body.innerHTML)
    cleanup()
  }, 60_000)
})
