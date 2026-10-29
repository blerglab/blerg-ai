import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { page } from 'vitest/browser'
import { MemoryRouter } from 'react-router-dom'
import { useTranscriptStore, useWorkingLineStore } from '@blerglab/chat'
import SessionRail from './SessionRail'
import { makeSession } from '../test/fixtures'
import { makeLive } from '../test/planTabFixture'
import type { LiveChildSession } from '../lib/liveState'
import type { LiveContext, LiveUsage } from '../lib/liveUsage'
import '../index.css'

vi.mock('../ws', () => import('../test/wsMock'))
vi.mock('../apiFetch', () => ({ apiFetch: vi.fn().mockResolvedValue({ ok: false }) }))

// Card #191: a context meter on every Running now row (Main, a Claude and a Codex started session)
// beside the token chip, and in the Session section; amber near compaction, ⇊ after one; none when
// the engine reported nothing (Hermes) or for a viewer who may not see tokens.
const usage = (total: number, context?: LiveContext): LiveUsage => ({ total, input: total / 10, output: total / 10, cache_read: total * 0.8, cache_write: 0, subagents: 0, turns: 3,
  turn: { tokens: total, input: total / 10, output: total / 10, cache_read: total * 0.8, cache_write: 0, in_flight: false }, ...(context ? { context } : {}) })

function show(width: number, owner: boolean) {
  const session = makeSession({ status: 'running', engine: 'claude' })
  const now = Date.now()
  useTranscriptStore.setState({ sessions: {} })
  useWorkingLineStore.setState({ lines: { [session.id]: { since: now - 60_000, waitingOn: null, activity: null } } })
  const child = (id: string, engine: string, total: number, context?: LiveContext): LiveChildSession => ({ id, title: `${engine} worker with a long title that has to be cut`, engine, model: 'fixture-model', status: 'running',
    started_at: new Date(now - 120_000).toISOString(), asking: false, ...(owner ? { tokens: total, usage_status: 'reported', usage: usage(total, context) } : { tokens: null, usage_status: 'hidden' }) })
  render(<MemoryRouter><div style={{ display: 'flex', height: 1400, width }}>
    <SessionRail id="rail" session={session} live={{ ...makeLive(), reasoningTokens: owner, usage: owner ? usage(1_412_000, { tokens: 124_000, window: 200_000, compacted: false }) : null }}
      tasks={[]} childSessions={[
        child('claude-child', 'claude', 180_000, { tokens: 30_000, window: 200_000, compacted: true, compacted_from: 170_000 }),
        child('codex-child', 'codex', 52_000, { tokens: 240_000, window: 272_000, compacted: false }),
        child('hermes-child', 'hermes', 9_000),
      ]} toolChanges={0} mode={width < 700 ? 'drawer' : 'panel'} open onClose={() => {}} onOpenArtifact={() => {}} />
  </div></MemoryRouter>)
  return screen.getByTestId('rail-running')
}

beforeEach(() => localStorage.clear())

describe('a context meter on every Running now row, in Chromium', () => {
  it.each([390, 3840])('at %i px for the owner: Main, a Claude, a Codex and a Hermes child, and the Session section', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const running = show(width, true)
    const main = within(running).getByTestId('rail-main')
    const meter = within(main).getByTestId('rail-main-context')
    expect(meter).toHaveTextContent('62%')
    expect(meter).toHaveAccessibleName(/^62% context: 124k of 200k tokens/)
    expect(meter.getAttribute('title')).toContain('Compaction threshold: not reported by Claude')
    expect(meter).not.toHaveClass('warn')
    const children = within(running).getAllByTestId('rail-child')
    const [claude, codex, hermes] = children
    const compacted = within(claude).getByTestId('rail-child-context')
    expect(compacted).toHaveTextContent('⇊15%')
    expect(within(claude).getByTestId('rail-child-context-compacted')).toBeVisible()
    expect(compacted.getAttribute('title')).toContain('Compacted in its latest turn (from 170k tokens)')
    const near = within(codex).getByTestId('rail-child-context')
    expect(near).toHaveTextContent('88%')
    expect(near).toHaveClass('warn')
    expect(getComputedStyle(near).color).not.toBe(getComputedStyle(meter).color)
    // No report, no meter: the token chip says the engine did not report it.
    expect(within(hermes).queryByTestId('rail-child-context')).toBeNull()
    expect(within(hermes).getByTestId('rail-child-tokens').getAttribute('title')).toContain('Context: not reported by Hermes')
    // Aligned: the meter has one width whatever its share, sits left of the token chip, inside its card.
    const meters = [meter, compacted, near]
    const widths = meters.map(m => Math.round(m.getBoundingClientRect().width))
    expect(new Set(widths).size).toBe(1)
    for (const [row, m] of [[main, meter], [claude, compacted], [codex, near]] as const) {
      const tokens = within(row).getByTestId(row === main ? 'rail-main-tokens' : 'rail-child-tokens')
      const box = row.getBoundingClientRect(), r = m.getBoundingClientRect()
      expect(r.right).toBeLessThanOrEqual(tokens.getBoundingClientRect().left)
      expect(Math.abs(r.top - tokens.getBoundingClientRect().top)).toBeLessThan(1)
      expect(r.right).toBeLessThanOrEqual(box.right + 0.5)
      expect(r.top).toBeGreaterThanOrEqual(box.top - 0.5)
      expect(r.bottom).toBeLessThanOrEqual(box.bottom + 0.5)
    }
    // The Session section: a bar, the share and the sizes.
    const fact = within(screen.getByTestId('rail-session')).getByTestId('rail-context')
    expect(fact).toHaveTextContent('62% · 124k of 200k')
    expect(within(fact).getByRole('meter', { name: 'Context in use' })).toHaveAttribute('aria-valuenow', '62')
    expect(fact.getBoundingClientRect().right).toBeLessThanOrEqual(screen.getByTestId('rail-session').getBoundingClientRect().right + 0.5)
    // Static facts first, then what changes (#191 review): below Started, above the model and effort.
    const section = screen.getByTestId('rail-session')
    const started = within(section).getByTestId('rail-timing')
    const model = within(section).getAllByRole('combobox')[0]
    expect(started.compareDocumentPosition(fact) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(fact.compareDocumentPosition(model) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(fact.getBoundingClientRect().top).toBeGreaterThanOrEqual(started.getBoundingClientRect().bottom - 0.5)
    expect(fact.getBoundingClientRect().bottom).toBeLessThanOrEqual(model.getBoundingClientRect().top + 0.5)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width)
    const shots = import.meta.env.VITE_CHROME_SHOTS_DIR
    if (shots) {
      await page.screenshot({ path: `${shots}/context-meter-running-${width}.png`, element: running })
      await page.screenshot({ path: `${shots}/context-meter-session-${width}.png`, element: screen.getByTestId('rail-session') })
    }
  })

  it('shows no meter to a viewer who may not see tokens (as Insights)', async () => {
    await page.viewport(1280, 900)
    const running = show(1280, false)
    expect(within(running).queryByTestId('rail-main-context')).toBeNull()
    expect(within(running).queryByTestId('rail-child-context')).toBeNull()
    expect(within(screen.getByTestId('rail-session')).queryByTestId('rail-context')).toBeNull()
  })
})
