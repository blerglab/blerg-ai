import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, waitFor, fireEvent, within } from '@testing-library/react'
import SessionInsights, { SESSION_INSIGHTS_REFRESH_MS } from './SessionInsights'
import type { SessionInsightsResponse } from '../lib/insights'

const apiFetch = vi.hoisted(() => vi.fn())
vi.mock('../apiFetch', () => ({ apiFetch }))

const tok = (input: number, output: number, cache_read: number, cache_write: number, cost_usd: number | null, turns = 0) =>
  ({ turns, input, output, cache_read, cache_write, cost_usd })

function fixture(over: Partial<SessionInsightsResponse> = {}): SessionInsightsResponse {
  return {
    session_id: 'sess-a', title: 'Fix the bug', kind: 'agent', status: 'idle', runtime: 'cluster', engine: 'claude',
    model: 'claude-opus-5-5', started_at: '2026-10-14T09:00:00Z', ended_at: null, generated_at: '2026-10-14T10:00:00Z',
    lifetime_seconds: 3600, running_seconds: 1200, waiting_seconds: 300, idle_seconds: 2100,
    turns: 12, tool_calls: 48,
    tool_calls_by_tool: [
      { key: 'Bash', count: 20 }, { key: 'Read', count: 12 }, { key: 'Edit', count: 8 }, { key: 'Grep', count: 4 },
      { key: 'Write', count: 2 }, { key: 'Glob', count: 1 }, { key: 'WebFetch', count: 1 },
    ],
    messages_from_person: 5,
    subagents: { count: 3, by_type: [{ key: 'general-purpose', count: 2 }, { key: 'Explore', count: 1 }], by_model: [{ key: 'm-sub', count: 3 }], tool_calls: 17 },
    tokens: {
      totals: tok(2100, 44000, 1_500_000, 90000, 4.2, 12),
      main: tok(2000, 40000, 1_400_000, 80000, 4.2, 12),
      subagents: tok(100, 4000, 100_000, 10000, null),
      by_model: [
        { model: 'm-main', priced: true, ...tok(2000, 40000, 1_400_000, 80000, 4.2, 12) },
        { model: 'm-sub', priced: false, ...tok(100, 4000, 100_000, 10000, null) },
      ],
      cache_hit_rate: 0.94,
      unpriced_models: ['m-sub'],
    },
    artifacts: 2,
    proposals: { pending: 1, approved: 2, declined: 0, expired: 0 },
    mcp_calls: { total: 7, by_outcome: [{ key: 'ok', count: 6 }, { key: 'refused', count: 1 }] },
    ...over,
  }
}

function json(body: unknown, status = 200) {
  return Promise.resolve({ ok: status < 400, status, json: () => Promise.resolve(body) } as Response)
}

describe('SessionInsights', () => {
  beforeEach(() => {
    apiFetch.mockReset()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('opens as a dialog titled after the session, with a skeleton until the report arrives', async () => {
    apiFetch.mockImplementation(() => json(fixture()))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    const dialog = screen.getByRole('dialog', { name: 'Fix the bug · Insights' })
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(screen.getByRole('status')).toHaveTextContent('Loading insights')
    await screen.findByTestId('si-time')
    expect(apiFetch).toHaveBeenCalledWith('/api/sessions/sess-a/insights')
    expect(screen.queryByRole('status')).toBeNull()

    // the header strip: state (icon and word), runtime, engine, model, and when the figures were read
    const meta = screen.getByTestId('session-insights-meta')
    const chips = within(meta).getAllByRole('listitem').map((li) => li.textContent)
    expect(chips).toEqual(['○idle', 'cluster', 'claude', 'opus-5-5'])
    expect(screen.getByTestId('si-updated')).toHaveTextContent('updated just now')
  })

  it('says "working in background", not idle, for an idle session with background work running', async () => {
    apiFetch.mockImplementation(() => json(fixture()))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live background onClose={() => {}} />)
    const state = await screen.findByTestId('si-state')
    expect(state).toHaveTextContent('working in background')
    expect(state).toHaveClass('si-state-background')
    expect(state).not.toHaveTextContent('idle')
  })

  it('leads with four headline tiles', async () => {
    apiFetch.mockImplementation(() => json(fixture()))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    await screen.findByTestId('si-kpis')
    const tiles = within(screen.getByTestId('si-kpis')).getAllByTestId(/^si-tile-/).map((t) => t.dataset.testid)
    expect(tiles).toEqual(['si-tile-lifetime', 'si-tile-turns', 'si-tile-tools', 'si-tile-tokens'])
    expect(screen.getByTestId('si-tile-lifetime')).toHaveTextContent(/^Lifetime1 hsince /)
    expect(screen.getByTestId('si-tile-turns')).toHaveTextContent('Turns12')
    expect(screen.getByTestId('si-tile-tools')).toHaveTextContent('Tool calls48+17 by subagents')
    // input + output, then the estimated cost (some models unpriced) and the cache reads
    expect(screen.getByTestId('si-tile-tokens')).toHaveTextContent('Tokens46.1kest. $4.20 + unpriced · cache 1.5M')
    // on a phone's narrow tile that subtitle wraps onto a second line, part by part, instead of being cut short
    const sub = screen.getByTestId('si-tile-tokens').querySelector('.si-tile-sub')!
    expect(sub).toHaveClass('si-tile-sub-parts')
    expect(Array.from(sub.children).map((p) => p.textContent)).toEqual(['est. $4.20 + unpriced', '· cache 1.5M'])
    expect(screen.getByTestId('si-tile-lifetime').querySelector('.si-tile-sub')).not.toHaveClass('si-tile-sub-parts')
  })

  it('splits the time in one bar with a legend carrying every duration', async () => {
    apiFetch.mockImplementation(() => json(fixture()))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    const time = await screen.findByTestId('si-time')
    const bar = within(time).getByRole('img')
    expect(bar.getAttribute('aria-label')).toBe('Working 20 min, Waiting on you 5 min, Idle 35 min')
    const segs = Array.from(bar.children) as HTMLElement[]
    expect(segs.map((s) => s.dataset.testid)).toEqual(['si-time-bar-running', 'si-time-bar-waiting', 'si-time-bar-idle'])
    const widths = segs.map((s) => parseFloat(s.style.width))
    expect(widths[0]).toBeCloseTo(100 * 1200 / 3600)
    expect(widths[1]).toBeCloseTo(100 * 300 / 3600)
    expect(widths[2]).toBeCloseTo(100 * 2100 / 3600)
    // the percent rides inside a segment only when it is at least 15% of the bar
    expect(segs.map((s) => s.textContent)).toEqual(['33%', '', '58%'])
    const legend = within(time).getAllByRole('listitem').map((li) => li.textContent)
    expect(legend).toEqual(['Working20 min', 'Waiting on you5 min', 'Idle35 min'])
  })

  it('lists tokens by model, largest first, then the main agent and subagent split and the totals', async () => {
    apiFetch.mockImplementation(() => json(fixture({
      tokens: {
        ...fixture().tokens!,
        by_model: [
          { model: 'm-sub', priced: false, ...tok(100, 4000, 100_000, 10000, null) },
          { model: 'm-main', priced: true, ...tok(2000, 40000, 1_400_000, 80000, 4.2, 12) },
        ],
      },
    })))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    const card = await screen.findByTestId('si-tokens')
    expect(within(card).getByRole('heading')).toHaveTextContent('Tokens by model')
    const rows = within(card).getAllByTestId('si-model-row')
    expect(rows.map((r) => r.textContent)).toEqual(['m-main1.52M', 'm-subno price114k'])
    const bars = rows.map((r) => parseFloat((r.querySelector('.si-bar') as HTMLElement).style.width))
    expect(bars[0]).toBe(100)
    expect(bars[1]).toBeCloseTo(100 * 114_100 / 1_522_000)
    // main agent first, subagents second, in one stacked bar
    const split = screen.getByTestId('si-token-split')
    expect(Array.from(split.children).map((s) => (s as HTMLElement).dataset.testid)).toEqual(['si-token-split-main', 'si-token-split-subagents'])
    expect(parseFloat((split.children[0] as HTMLElement).style.width)).toBeCloseTo(100 * 1_522_000 / 1_636_100)
    expect(split.getAttribute('aria-label')).toBe('Main agent 1.52M, Subagents 114k')
    const legend = within(screen.getByTestId('si-token-split-legend')).getAllByRole('listitem').map((li) => li.textContent)
    expect(legend).toEqual(['Main agent1.52Mest. $4.20', 'Subagents114kunpriced'])
    const figs = screen.getByTestId('si-token-figs')
    expect(figs).toHaveTextContent('Input2.1k')
    expect(figs).toHaveTextContent('Output44k')
    expect(figs).toHaveTextContent('Cache read / write1.5M / 90k')
    expect(figs).toHaveTextContent('Cache hit rate94%')
    expect(within(card).queryByRole('table')).toBeNull()
  })

  it('shows the top five tools and folds the rest, beside MCP calls and proposals', async () => {
    apiFetch.mockImplementation(() => json(fixture()))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    await screen.findByTestId('si-work')
    const tools = screen.getAllByTestId('si-tool-row')
    expect(tools.map((r) => r.textContent)).toEqual(['Bash20', 'Read12', 'Edit8', 'Grep4', 'Write2'])
    expect(parseFloat((tools[1].querySelector('.si-bar') as HTMLElement).style.width)).toBeCloseTo(60)
    expect(screen.getByTestId('si-tools-other')).toHaveTextContent('other tools · 2')
    expect(screen.getByTestId('si-mcp')).toHaveTextContent('MCP calls7ok 6refused 1')
    // proposals: only the outcomes that happened
    expect(screen.getByTestId('si-proposals')).toHaveTextContent('Proposals3pending 1approved 2')
    expect(screen.getByTestId('si-proposals')).not.toHaveTextContent('declined')
    expect(screen.getByTestId('si-work-line')).toHaveTextContent('5 messages from you · 2 files published')
  })

  it('tabulates subagents by type and model, or says there were none', async () => {
    apiFetch.mockImplementation(() => json(fixture()))
    const { unmount } = render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    const card = await screen.findByTestId('si-subagents')
    expect(within(card).getByRole('heading')).toHaveTextContent('Subagents · 3')
    expect(card).toHaveTextContent('17 tool calls · 114k tokens')
    const types = within(screen.getByTestId('si-sub-types')).getAllByRole('row').map((r) => r.textContent)
    expect(types).toEqual(['TypeCount', 'general-purpose2', 'Explore1'])
    expect(within(screen.getByTestId('si-sub-models')).getAllByRole('row')[1]).toHaveTextContent('m-sub3')
    unmount()

    apiFetch.mockImplementation(() => json(fixture({ subagents: { count: 0, by_type: [], by_model: [], tool_calls: 0 } })))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    const none = await screen.findByTestId('si-subagents')
    expect(none).toHaveTextContent('Subagents · 0No subagents')
    expect(within(none).queryByRole('table')).toBeNull()
    expect(screen.getByTestId('si-tile-tools')).toHaveTextContent(/^Tool calls48$/)
  })

  it('shows what applies to a terminal session', async () => {
    apiFetch.mockImplementation(() => json(fixture({
      kind: 'tmux', running_seconds: 0, waiting_seconds: 0, idle_seconds: 0, turns: 0, tool_calls: 0, tool_calls_by_tool: [],
      subagents: { count: 0, by_type: [], by_model: [], tool_calls: 0 }, artifacts: 1,
      proposals: { pending: 0, approved: 0, declined: 0, expired: 0 }, mcp_calls: { total: 0, by_outcome: [] },
    })))
    render(<SessionInsights sessionId="sess-a" title="Shell" live={false} onClose={() => {}} />)
    await screen.findByTestId('si-kpis')
    expect(within(screen.getByTestId('si-kpis')).getAllByTestId(/^si-tile-/)).toHaveLength(1)
    expect(screen.getByTestId('si-tile-lifetime')).toHaveTextContent('Lifetime1 h')
    expect(screen.getByTestId('si-work-line')).toHaveTextContent('1 file published')
    expect(screen.getByTestId('si-work-line')).not.toHaveTextContent('messages')
    expect(screen.queryByTestId('si-time')).toBeNull()
    expect(screen.queryByTestId('si-tile-turns')).toBeNull()
    expect(screen.queryByTestId('si-top-tools')).toBeNull()
    expect(screen.queryByTestId('si-subagents')).toBeNull()
    expect(screen.queryByTestId('si-tokens')).toBeNull()
    expect(screen.getByTestId('session-insights-terminal')).toHaveTextContent('A terminal session runs the engine’s own CLI')
  })

  it('says token figures are the owner\'s and administrators\' when the report hides them', async () => {
    apiFetch.mockImplementation(() => json(fixture({ tokens: null, tokens_hidden: true })))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    await screen.findByTestId('si-time')
    expect(screen.getByTestId('si-tokens-hidden')).toHaveTextContent('Token figures are visible to the session’s owner and administrators.')
    expect(screen.queryByTestId('si-models')).toBeNull()
    expect(screen.queryByTestId('si-token-split')).toBeNull()
    expect(screen.getByTestId('si-tile-tokens')).toHaveTextContent('Tokens—owner and admins only')
    // the rest stays: subagents without their tokens, work as before
    expect(screen.getByTestId('si-subagents')).toHaveTextContent('Subagents · 3')
    expect(screen.getByTestId('si-subagents')).not.toHaveTextContent('tokens')
    expect(screen.getByTestId('si-tile-turns')).toHaveTextContent('12')
  })

  it('never shows one session\'s figures under another, and reads once more when the session ends', async () => {
    let resolveB: (v: Response) => void = () => {}
    apiFetch.mockImplementation((url: string) => (url.includes('sess-b')
      ? new Promise<Response>((r) => { resolveB = r })
      : json(fixture())))
    const { rerender } = render(<SessionInsights sessionId="sess-a" title="A" live onClose={() => {}} />)
    await screen.findByTestId('si-time')
    rerender(<SessionInsights sessionId="sess-b" title="B" live onClose={() => {}} />)
    expect(screen.queryByTestId('si-time')).toBeNull()
    expect(screen.getByRole('status')).toHaveTextContent('Loading insights')
    await act(async () => { resolveB(await json(fixture({ session_id: 'sess-b', turns: 99 }))) })
    expect(await screen.findByTestId('si-tile-turns')).toHaveTextContent('99')
    const before = apiFetch.mock.calls.length
    rerender(<SessionInsights sessionId="sess-b" title="B" live={false} onClose={() => {}} />)
    await waitFor(() => expect(apiFetch.mock.calls.length).toBe(before + 1))
  })

  it('says when it cannot load, and Retry asks again', async () => {
    apiFetch.mockImplementationOnce(() => json({ error: 'boom' }, 500))
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Could not load insights for this session.')
    apiFetch.mockImplementation(() => json(fixture()))
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }))
    await screen.findByTestId('si-time')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(apiFetch).toHaveBeenCalledTimes(2)
  })

  it('closes on Escape, the close button and the backdrop', async () => {
    apiFetch.mockImplementation(() => json(fixture()))
    const onClose = vi.fn()
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={onClose} />)
    fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledTimes(2)
    const overlay = screen.getByTestId('session-insights-overlay')
    fireEvent.pointerDown(overlay)
    fireEvent.click(overlay)
    expect(onClose).toHaveBeenCalledTimes(3)
    await screen.findByTestId('si-time')
  })

  it('refreshes every 30 s while the session is live, and not once it is over', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] })
    apiFetch.mockImplementation(() => json(fixture()))
    const { unmount } = render(<SessionInsights sessionId="sess-a" title="Fix the bug" live onClose={() => {}} />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    await act(async () => { await vi.advanceTimersByTimeAsync(SESSION_INSIGHTS_REFRESH_MS) })
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(2))
    unmount()

    apiFetch.mockClear()
    render(<SessionInsights sessionId="sess-a" title="Fix the bug" live={false} onClose={() => {}} />)
    await waitFor(() => expect(apiFetch).toHaveBeenCalledTimes(1))
    await act(async () => { await vi.advanceTimersByTimeAsync(SESSION_INSIGHTS_REFRESH_MS * 2) })
    expect(apiFetch).toHaveBeenCalledTimes(1)
  })
})
