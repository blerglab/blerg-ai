import { afterEach, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { cdp, page, userEvent } from 'vitest/browser'
import { Markdown } from '@blerglab/chat'
import '../index.css'
import '@blerglab/chat/blerg.css'

async function touch(enabled: boolean) {
  const client = cdp() as unknown as { send(method: string, params: object): Promise<unknown> }
  await client.send('Emulation.setTouchEmulationEnabled', { enabled, maxTouchPoints: 1 })
  await client.send('Emulation.setEmulatedMedia', { features: enabled ? [{ name: 'hover', value: 'none' }, { name: 'pointer', value: 'coarse' }] : [] })
}
afterEach(async () => { await touch(false) })

function noHeaderOverlap(actions: HTMLElement) {
  const box = actions.getBoundingClientRect()
  for (const header of document.querySelectorAll('th')) {
    const range = document.createRange()
    range.selectNodeContents(header)
    for (const text of range.getClientRects()) {
      expect(box.bottom <= text.top || box.top >= text.bottom || box.right <= text.left || box.left >= text.right, header.textContent ?? '').toBe(true)
    }
  }
}

it.each([390, 3840])('keeps touch export controls and the open toolbar above header text at %ipx', async width => {
  await touch(true)
  await page.viewport(width, width === 390 ? 844 : 2160)
  expect(matchMedia('(hover: none)').matches).toBe(true)
  render(<div className="agent-card assistant" style={{ width: 350 }}><Markdown text={'| Piece | Description | Effort |\n| --- | --- | --- |\n| Import | Import existing records. | Small |'} /></div>)
  const trigger = screen.getByRole('button', { name: 'Table actions' })
  noHeaderOverlap(trigger)
  await userEvent.click(trigger)
  noHeaderOverlap(screen.getByRole('toolbar', { name: 'Table export actions' }))
  expect(screen.getByRole('button', { name: 'Download CSV' })).toBeVisible()
  const shots = (import.meta.env as Record<string, string | undefined>).VITE_THEME_SHOTS_DIR
  if (shots) await page.screenshot({ path: `${shots}/tables-touch-${width}.png` })
})

it('keeps table controls stationary and reachable after scrolling 200px', async () => {
  await touch(true)
  await page.viewport(390, 844)
  const labels = Array.from({ length: 12 }, (_, index) => `Column${index}`)
  render(<div className="agent-card assistant" style={{ width: 350 }}><Markdown text={`| ${labels.join(' | ')} |\n| ${labels.map(() => '---').join(' | ')} |\n| ${labels.map(() => 'Ready').join(' | ')} |`} /></div>)
  const trigger = screen.getByRole('button', { name: 'Table actions' })
  const wrap = document.querySelector<HTMLElement>('.md-table-wrap')!
  const scroller = wrap.querySelector<HTMLElement>('.md-table-scroll') ?? wrap
  const before = trigger.getBoundingClientRect().left
  scroller.scrollLeft = 200
  expect(scroller.scrollLeft).toBe(200)
  expect(trigger.getBoundingClientRect().left).toBeCloseTo(before, 0)
  await userEvent.click(trigger)
  expect(screen.getByRole('button', { name: 'Download CSV' })).toBeVisible()
})
