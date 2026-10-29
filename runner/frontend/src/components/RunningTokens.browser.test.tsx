import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { useTranscriptStore, useWorkingLineStore } from '@blerglab/chat'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import type { ProgressSubagent } from '../lib/liveState'
import type { LiveChildSession } from '../lib/liveState'
import type { LiveUsage } from '../lib/liveUsage'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn().mockResolvedValue({ ok: false }) }))

// Card #233: a token chip on every Running now row (Main, its subagents, Claude and Codex started
// sessions), the total compact and the breakdown in its tooltip, for whoever may see tokens.
const mainUsage: LiveUsage = { total: 1_412_000, input: 12_000, output: 45_000, cache_read: 1_300_000, cache_write: 55_000, subagents: 205_000, turns: 9,
  turn: { tokens: 180_000, input: 2_000, output: 8_000, cache_read: 170_000, cache_write: 0, in_flight: false }, cost_usd: 4.2 }
const childUsage = (total: number): LiveUsage => ({ total, input: total / 10, output: total / 10, cache_read: total * 0.8, cache_write: 0, subagents: 0, turns: 1,
  turn: { tokens: total, input: total / 10, output: total / 10, cache_read: total * 0.8, cache_write: 0, in_flight: false } })

function show(width: number, owner: boolean) {
  const session = makeSession({ status: 'running', engine: 'claude' })
  const now = Date.now()
  useTranscriptStore.setState({ sessions: {} })
  useWorkingLineStore.setState({ lines: { [session.id]: { since: now - 60_000, waitingOn: null, activity: null } } })
  const child = (id: string, engine: string, total: number): LiveChildSession => ({ id, title: `${engine} worker`, engine, model: 'fixture-model', status: 'running',
    started_at: new Date(now - 120_000).toISOString(), asking: false, ...(owner ? { tokens: total, usage_status: 'reported', usage: childUsage(total) } : { tokens: null, usage_status: 'hidden' }) })
  const pending: LiveChildSession = { id: 'pending', title: 'first turn', engine: 'claude', model: '', status: 'running', started_at: new Date(now - 10_000).toISOString(), asking: false, tokens: null, usage_status: owner ? 'pending' : 'hidden' }
  render(<MemoryRouter><div style={{ display: 'flex', height: 900, width }}>
    <SessionRail id="rail" session={session} live={{ ...makeLive({ subagents: [
      ({ key: 'S1', title: 'Survey the layout', type: 'Explore', model: '', tokens: 205_000, usage: { input: 5000, output: 10_000, cache_read: 190_000, cache_write: 0 }, doing: 'Read · src/index.ts', startedAt: now - 30_000, background: false } as ProgressSubagent),
      { key: 'S2', title: 'Just started', type: 'Explore', model: '', tokens: null, doing: '', startedAt: now - 2_000, background: false },
    ] }),
      reasoningTokens: owner, usage: owner ? mainUsage : null }}
      tasks={[]} childSessions={[child('claude-child', 'claude', 180_000), child('codex-child', 'codex', 52_000), pending]} toolChanges={0} mode={width < 700 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}} />
  </div></MemoryRouter>)
  return screen.getByTestId('rail-running')
}

beforeEach(() => localStorage.clear())

describe('a token chip on every Running now row, in Chromium', () => {
  it.each([390, 3840])('at %i px for the owner: Main, the subagent, a Claude and a Codex child', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const running = show(width, true)
    const main = within(running).getByTestId('rail-main')
    const chip = within(main).getByTestId('rail-main-tokens')
    expect(chip).toHaveTextContent('1.4M')
    const tip = chip.getAttribute('title')!
    expect(tip).toContain('Tokens so far: 1.4M tokens')
    expect(tip).toContain('Last turn: 180k tokens')
    expect(tip).toContain('Input 12k · cached 1.4M (1.3M read, 55k written) · output 45k')
    expect(tip).toContain('Subagents: 205k tokens of the total')
    expect(tip).toContain('Estimated cost: $4.20')
    // A subagent's chip has the same breakdown; one with nothing reported yet shows the dash.
    const subChips = within(running).getAllByTestId('rail-sub-tokens')
    expect(subChips.map(c => c.textContent)).toEqual(['205k', '–'])
    expect(subChips[0].getAttribute('title')!.split('\n')).toEqual(['Tokens so far: 205k tokens', 'This run so far: 205k tokens', 'Input 5.0k · cached 190k · output 10k'])
    expect(subChips[1].getAttribute('title')).toMatch(/none recorded yet/)
    const children = within(running).getAllByTestId('rail-child')
    const chips = children.map(c => within(c).getByTestId('rail-child-tokens'))
    expect(chips.map(c => c.textContent)).toEqual(['180k', '52k', '–'])
    expect(chips[1].getAttribute('title')).toContain('Input 5.2k · cached 42k · output 5.2k')
    expect(chips[2].getAttribute('title')).toMatch(/none recorded yet/)
    // Each chip sits inside its card, with the card's other glyphs.
    for (const [row, c] of [[main, chip], ...children.map((r, i) => [r, chips[i]] as const)] as const) {
      const box = row.getBoundingClientRect(), r = c.getBoundingClientRect()
      expect(r.width).toBeGreaterThan(0)
      expect(r.right).toBeLessThanOrEqual(box.right + 0.5)
      expect(r.top).toBeGreaterThanOrEqual(box.top - 0.5)
      expect(r.bottom).toBeLessThanOrEqual(box.bottom + 0.5)
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
    if (shots) await page.screenshot({ path: `${shots}/running-tokens-${width}.png` })
  })

  // The non-owner's subagent still carries a total, as the chat's own transcript reading (the
  // fallback when /live is unavailable) would: without the owner/admin decision it is not shown.
  it('shows no chip to a viewer who may not see tokens (as Insights), even with a transcript-derived total', async () => {
    await page.viewport(1280, 900)
    const running = show(1280, false)
    expect(within(running).queryByTestId('rail-main-tokens')).toBeNull()
    expect(within(running).queryByTestId('rail-child-tokens')).toBeNull()
    expect(within(running).queryByTestId('rail-sub-tokens')).toBeNull()
    expect(running.textContent).not.toMatch(/205k/)
  })
})
