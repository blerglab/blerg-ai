// A host that shows the chat's settings and Files in a place of its own (the runner's session panel):
// the compact-tools choice as a prop, the toolbar without its Files button, and the Files list,
// its operations and the message box's file picker reported for the host to use.
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import ChatView from './ChatView'
import FilesList from './FilesList'
import { createFakeTransport } from '../test/fakeTransport'
import { useChatReportStore } from '../hooks/useLiveState'
import { COMPACT, SESSION, ev, meta, resetChatState, seed } from '../test/chat'
import type { ArtifactInfo } from '../transport/types'
import type { AgentEvent } from '../types'

const live = meta({ status: 'idle' })
const report: ArtifactInfo = { id: 'a1', name: 'report.md', size: 2048, content_type: 'text/markdown', view: 'markdown' }

// Three finished Bash calls: one summary row when folded, three cards when not.
function bashRun(): AgentEvent[] {
  const out: AgentEvent[] = []
  for (let i = 1; i <= 3; i++) {
    out.push(ev({ seq: i * 2 - 1, kind: 'tool_call', payload: { tool: 'Bash', call_id: `c${i}`, input: { command: `echo ${i}` } } }))
    out.push(ev({ seq: i * 2, kind: 'tool_result', payload: { call_id: `c${i}`, output: 'ok', is_error: false, duration_ms: 1 } }))
  }
  return out
}

describe('ChatView: a host that holds the compact choice', () => {
  beforeEach(() => resetChatState())
  afterEach(() => localStorage.removeItem(COMPACT))

  it('has no Compact tool calls checkbox and folds as the prop says, whatever storage holds', () => {
    localStorage.setItem(COMPACT, '0')
    seed(...bashRun())
    const { rerender } = render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} compact />)
    expect(screen.queryByRole('checkbox', { name: 'Compact tool calls' })).toBeNull()
    expect(screen.getByTestId('tool-group')).toBeInTheDocument()
    rerender(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} compact={false} />)
    expect(screen.queryByTestId('tool-group')).toBeNull()
    // The host keeps the choice: the chat writes nothing.
    expect(localStorage.getItem(COMPACT)).toBe('0')
    expect(useChatReportStore.getState().reports[SESSION].compact).toBe(false)
  })

  it('without the prop the toolbar keeps its checkbox, saved in storage, as before', () => {
    seed(...bashRun())
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} />)
    const box = screen.getByRole('checkbox', { name: 'Compact tool calls' })
    fireEvent.click(box)
    expect(localStorage.getItem(COMPACT)).toBe('0')
    expect(screen.queryByTestId('tool-group')).toBeNull()
  })
})

describe('ChatView: filesButton={false}', () => {
  beforeEach(() => {
    resetChatState()
    seed(ev({ seq: 1, payload: { text: 'hello', source: 'human' } }))
  })

  it('has no Files button and never opens its dialog; the report carries the list and what it can do', async () => {
    const t = createFakeTransport({ files: { items: [report] } })
    render(<ChatView session={SESSION} transport={t.transport} meta={live} filesButton={false} />)
    expect(screen.queryByTestId('files-toggle')).toBeNull()
    await waitFor(() => expect(useChatReportStore.getState().reports[SESSION].files?.items).toEqual([report]))
    const files = useChatReportStore.getState().reports[SESSION].files!
    expect(files.status).toBe('ready')
    expect(files.ops).toBe(t.files)
    // No dialog to open: openFiles is not reported.
    expect(useChatReportStore.getState().reports[SESSION].openFiles).toBeUndefined()

    // refresh reads the list again: a file added elsewhere comes in.
    t.files!.items = [{ ...report, id: 'a2', name: 'notes.md' }, report]
    act(() => files.refresh())
    await waitFor(() => expect(useChatReportStore.getState().reports[SESSION].files?.items.map(a => a.id)).toEqual(['a2', 'a1']))
  })

  it('attach opens the message box’s file picker', () => {
    render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} filesButton={false} />)
    const input = screen.getByTestId('attach-input') as HTMLInputElement
    let clicked = 0
    input.addEventListener('click', () => { clicked++ })
    act(() => useChatReportStore.getState().reports[SESSION].files!.attach!())
    expect(clicked).toBe(1)
  })

  it('a read-only chat reports no picker; a transport without files reports no files', () => {
    const { unmount } = render(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={live} readOnly />)
    expect(useChatReportStore.getState().reports[SESSION].files?.attach).toBeUndefined()
    unmount()
    render(<ChatView session={SESSION} transport={createFakeTransport({ files: false }).transport} meta={live} />)
    expect(useChatReportStore.getState().reports[SESSION].files).toBeUndefined()
  })
})

describe('FilesList', () => {
  it('is the Files panel’s list without the dialog, with the host’s retry wording', () => {
    const t = createFakeTransport()
    const { rerender } = render(<FilesList sessionId={SESSION} files={t.files!} items={[report]} status="ready" onView={() => {}} onChanged={() => {}} />)
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByTestId('artifact-name')).toHaveTextContent('report.md')
    rerender(<FilesList sessionId={SESSION} files={t.files!} items={[]} status="error" onView={() => {}} onChanged={() => {}} retryHint="Open this tab again to retry." />)
    expect(screen.getByRole('alert')).toHaveTextContent('Couldn’t load the files. Open this tab again to retry.')
  })
})
