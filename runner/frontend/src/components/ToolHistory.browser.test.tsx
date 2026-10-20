import type { CSSProperties } from 'react'
import { describe, expect, it } from 'vitest'
import { fireEvent, render } from '@testing-library/react'
import { ChatView, ToolCard, ToolGroup, renderEvent, type AgentEvent, type ToolEntry } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import { meta, resetChatState, seed, SESSION } from '../../../../packages/chat/src/test/chat'
import { createFakeTransport } from '../../../../packages/chat/src/test/fakeTransport'

const palette = {
  '--chat-bg': '#101820', '--chat-surface': '#263442', '--chat-fg': '#e0e8f0',
  '--chat-muted': '#a0aab4', '--chat-muted-2': '#8296aa', '--chat-danger': '#f02030', '--chat-success': '#30d080',
} as CSSProperties
const danger = 'rgb(240, 32, 48)'
const success = 'rgb(48, 208, 128)'
function entry(index: number, failed?: boolean): ToolEntry {
  const call = { call_id: `attempt-${index}`, tool: 'Bash', input: { command: `check-${index}` } }
  const ev: AgentEvent = { type: 'agent_event', session_id: SESSION, client_event_id: `event-${index}`, seq: index,
    kind: 'tool_call', ts: '2026-01-01T00:00:00Z', payload: call }
  return { ev, call, resultOrder: index, ...(failed === undefined ? {} : {
    result: { call_id: call.call_id, is_error: failed, output: failed ? 'Recorded command failed: diagnostic detail' : 'ok', duration_ms: 12 },
  }) }
}
function host(children: React.ReactNode) {
  return render(<div className="chat-root" style={palette}>{children}</div>)
}
const color = (element: Element) => getComputedStyle(element).color

describe('recorded tool attempts use neutral history styling', () => {
  it.each([
    ['earlier failure', [entry(1, true), entry(2, false), entry(3, false)], 2, 1],
    ['latest failure', [entry(1, false), entry(2, false), entry(3, true)], 2, 1],
    ['all failed', [entry(1, true), entry(2, true), entry(3, true)], 0, 3],
  ] as const)('%s keeps neutral group/expanded details and accurate counts', (_label, entries, succeeded, failed) => {
    const view = host(<ToolGroup entries={[...entries]} live={new Set()} />)
    const group = view.getByTestId('tool-group')
    expect(getComputedStyle(group).borderLeftColor).toBe('rgb(38, 52, 66)')
    expect(color(view.getByTestId('tool-fail-badge'))).toBe('rgb(130, 150, 170)')
    expect(view.getByTestId('tool-fail-badge').textContent).toContain(`${failed} failed`)
    expect(view.getByTestId('tool-success-count').textContent).toContain(`${succeeded} succeeded`)
    if (succeeded) expect(color(view.getByTestId('tool-success-count'))).toBe(success)
    fireEvent.click(view.getByTestId('tool-group-toggle'))
    const row = [...view.getAllByTestId('tool-row')].find(node => node.querySelector('[aria-label="failed"]'))!
    expect(color(row.querySelector('.tcg-name')!)).not.toBe(danger)
    expect(color(row.querySelector('[aria-label="failed"]')!)).not.toBe(danger)
    fireEvent.click(row.querySelector('button')!)
    const output = row.querySelector('.tcg-out')!
    expect(output.textContent).toContain('Recorded command failed: diagnostic detail')
    expect(color(output)).not.toBe(danger)
  })

  it('keeps individual failed rows and classic cards neutral, with failed status and details', () => {
    const failed = entry(1, true)
    const view = host(<><ToolGroup entries={[failed]} live={new Set()} /><ToolCard call={failed.call} result={failed.result} /></>)
    const row = view.getByTestId('tool-row')
    expect(color(row.querySelector('[aria-label="failed"]')!)).not.toBe(danger)
    expect(color(row.querySelector('.tcg-name')!)).not.toBe(danger)
    const card = view.getByTestId('tool-card')
    expect(color(card.querySelector('.tool-name')!)).not.toBe(danger)
    expect(card.textContent).toContain('✗')
    fireEvent.click(card.querySelector('button')!)
    expect(card.textContent).toContain('Recorded command failed: diagnostic detail')
  })

  it('preserves red explicit terminal subagent errors', () => {
    resetChatState()
    seed({ ...entry(8).ev, kind: 'subagent_started', payload: { child_id: 'failed-child', agent_type: 'helper', model: 'fixture', summary: 'A task' } },
      { ...entry(9).ev, kind: 'subagent_done', payload: { child_id: 'failed-child', summary: 'error: could not continue' } })
    const view = host(<ChatView session={SESSION} transport={createFakeTransport().transport} meta={meta()} />)
    const status = view.getByTestId('subagent-status')
    expect(status.getAttribute('aria-label')).toBe('error')
    expect(color(status.querySelector('.tcg-glyph')!)).toBe(danger)
  })

  it('preserves live/missing result status and red explicit run errors', () => {
    const live = entry(3)
    const error: AgentEvent = { ...entry(9).ev, kind: 'error', payload: { message: 'Action required: run could not continue', retryable: false } }
    const view = host(<><ToolGroup entries={[entry(1, true), entry(2, false), live]} live={new Set([live.call.call_id])} />{renderEvent(error, new Map())}</>)
    expect(view.getByRole('img', { name: 'running' })).not.toBeNull()
    expect(view.getByTestId('tool-success-count').textContent).toContain('1 succeeded')
    expect(view.getByTestId('tool-fail-badge').textContent).toContain('1 failed')
    const runError = view.container.querySelector('.agent-card.error')!
    expect(runError.textContent).toContain('Action required')
    expect(getComputedStyle(runError).borderLeftColor).toBe(danger)
  })
})
