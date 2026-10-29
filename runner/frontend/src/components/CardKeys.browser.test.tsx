import { afterEach, expect, it, vi } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { HOVERCARD, ReferenceScope, ReferenceText, resetRefStoresForTest, clearHovercardCache, type ChatRefs } from '@blerglab/chat'
import { installHoverClock } from '../test/hoverClock'
import '../index.css'

afterEach(() => { cleanup(); resetRefStoresForTest(); clearHovercardCache() })
it.each([390, 3840])('renders cross-board keys and rename aliases with hovercards at %i px', async width => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  const clock = installHoverClock()
  try {
    const openCard = vi.fn()
    const refs: ChatRefs = {
      boardCards: async (board, ns) => new Map(ns.map(n => [n, { kind:'card', source:'blerg-board', boardId:board === 'AD' ? 'board-b' : 'board-a', key:`${board === 'AD' ? 'AD' : 'NEW'}-${n}`, title:'Work' }])),
      openCard,
    }
    render(<ReferenceScope sessionId={`keys-${width}`} refs={refs}><ReferenceText text="(BLG-205/AD-6) and board:board-a#205" /></ReferenceScope>)
    await waitFor(() => expect(screen.getAllByTestId('md-ref')).toHaveLength(3))
    const chips = screen.getAllByTestId('md-ref')
    expect(chips.map(c => c.textContent)).toEqual(['NEW-205', 'AD-6', 'NEW-205'])
    await userEvent.keyboard('{Tab}')
    expect(chips[0]).toHaveFocus()
    await waitFor(() => expect(clock.pending()).toBeGreaterThan(0))
    clock.advance(HOVERCARD.openDelayMs)
    expect(await screen.findByTestId('hovercard')).toHaveTextContent('NEW-205')
    await userEvent.click(chips[0])
    expect(openCard).toHaveBeenCalledWith(expect.objectContaining({ key:'NEW-205', boardId:'board-a' }))
    for (const chip of chips) expect(chip.getBoundingClientRect().right).toBeLessThanOrEqual(width)
  } finally { clock.uninstall() }
})
