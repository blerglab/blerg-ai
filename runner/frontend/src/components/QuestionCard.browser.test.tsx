import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, within, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { AskReply, HOVERCARD } from '@blerglab/chat'
import '../index.css'
import '@blerglab/chat/blerg.css'
import './SessionRail.css'
import QuestionRow from './QuestionRow'

// QuestionRow now subscribes to live question state. Keep the component fixture off the real authenticated socket.
vi.mock('../ws', () => import('../test/wsMock'))

import { installHoverClock, type ManualHoverClock } from '../test/hoverClock'

let hoverClock: ManualHoverClock
beforeEach(() => { hoverClock = installHoverClock() })
afterEach(() => hoverClock.uninstall())

const shotDir = (import.meta.env as Record<string, string | undefined>).VITE_QUESTION_SHOT_DIR
for (const width of [390, 3840]) for (const long of [false, true]) for (const worker of [undefined, 'Ada']) {
  describe(`${width}px, ${long ? 'long' : 'short'}, ${worker ?? 'no worker'}`, () => {
    it('shows one plain row and one readable question, expanding only on request', async () => {
      await page.viewport(width, width === 390 ? 844 : 2160)
      const content = long ? 'Which approach should we take? ' + 'Please consider the migration, existing clients, and rollback plan before choosing. '.repeat(18) : 'Ship this change?'
      const question = long && worker ? `Child "${worker}", parent "Main": ${content}` : content
      render(<ul style={{ width: 'min(320px, 100%)', padding: 0, margin: 0 }}><li className="rail-item rail-ask"><QuestionRow id="ask" question={question} worker={worker} audience="you" wait="waiting 3 min">
        <p className="rail-item-kicker">not blocking · waiting 3 min</p>
        <div className="rail-actions"><button className="rail-link">Show in conversation</button></div>
        <AskReply className="rail-reply" draft="Ready to ship" onDraft={() => {}} answer={async () => true} messageId="ask" actions={<button type="button" className="rail-link rail-dismiss">Dismiss</button>} />
      </QuestionRow></li></ul>)
      const row = screen.getByRole('button', { name: /Ship this change|Which approach/ })
      expect(getComputedStyle(row).borderTopWidth).toBe('0px')
      expect(row.querySelector('.ticket-chip')).toBeNull()
      if (shotDir) await page.screenshot({ path: `${shotDir}/row-${width}-${long}-${worker ?? 'solo'}.png` })
      // Long question rows extend beyond the viewport. Move off the previous
      // case's anchor, then hover visible text instead of its offscreen midpoint.
      await userEvent.hover(document.documentElement, {position: {x: width - 1, y: 1}})
      await userEvent.hover(row, {position: {x: 16, y: 16}})
      await waitFor(() => expect(hoverClock.pending()).toBeGreaterThan(0))
      act(() => hoverClock.advance(HOVERCARD.openDelayMs))
      await waitFor(() => expect(screen.getByRole('tooltip')).toHaveTextContent(content.trim()), { timeout: 2000 })
      expect(screen.getByRole('tooltip')).toHaveTextContent(worker ?? 'Main')
      expect(screen.getByRole('tooltip')).toHaveTextContent('waiting 3 min')
      expect(row).toHaveClass('workspace-row')
      // Owner 2026-11-05: compact rows; Main's questions carry no 'Main · Needs you' line (the heading
      // says it), a worker's still names the worker.
      if (worker) expect(row.textContent).toContain(worker)
      else expect(row.textContent).not.toContain('Needs you')
      expect(row.textContent).not.toContain('open')
      expect(row.textContent).not.toContain('Child')
      if (long) expect(screen.getByRole('tooltip').querySelector('.question-preview-text')!.getBoundingClientRect().height).toBeLessThan(220)
      if (shotDir) await page.screenshot({ path: `${shotDir}/hover-${width}-${long}-${worker ?? 'solo'}.png` })
      await userEvent.click(row)
      const modal = screen.getByRole('dialog')
      expect(modal.querySelector('.ticket-modal-title')).toHaveTextContent(content.trim())
      expect(modal.getBoundingClientRect().width).toBeLessThanOrEqual(Math.min(width * 0.92, 960) + 1)
      for (const side of ['paddingTop', 'paddingRight', 'paddingBottom', 'paddingLeft'] as const) expect(getComputedStyle(modal)[side]).toBe('24px')
      expect(modal.scrollWidth).toBeLessThanOrEqual(modal.clientWidth)
      expect(within(modal).getByRole('heading', { name: content.trim() })).toBeVisible()
      expect(within(modal).getByRole('textbox', { name: 'Your answer' })).toHaveFocus()
      expect(modal.querySelector('.card-author')).toHaveTextContent(worker ?? 'Main')
      expect(within(modal).queryByRole('button', { name: 'Maximize' })).toBeNull()
      if (shotDir) await page.screenshot({ element: modal.parentElement!, path: `${shotDir}/modal-${width}-${long}-${worker ?? 'solo'}.png` })
      await userEvent.keyboard('{Escape}')
      expect(screen.queryByRole('dialog')).toBeNull()
      expect(document.activeElement).toBe(row)
    })
  })
}

for (const width of [390, 1920, 3840]) it(`bounds a binary decision at ${width}px`, async () => {
 await page.viewport(width, width === 390 ? 844 : 2160)
 render(<QuestionRow id="binary" question="Approve board link?" audience="you" worker="Main"><p>Link this workspace to the selected board so its decisions stay together.</p><AskReply binary answer={async()=>true} messageId="binary" /></QuestionRow>)
 await userEvent.click(screen.getByRole('button',{name:/Approve board link/}))
 const modal=screen.getByRole('dialog')
 expect(modal.getBoundingClientRect().width).toBeLessThanOrEqual(Math.min(width * .92,960)+1)
 expect(modal.scrollWidth).toBeLessThanOrEqual(modal.clientWidth)
 expect(within(modal).getByRole('button',{name:'Approve'})).toBeVisible()
 expect(within(modal).queryByRole('textbox')).toBeNull()
 if(shotDir) await page.screenshot({path:`${shotDir}/binary-${width}.png`})
})
