import { render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { commands, page, userEvent } from 'vitest/browser'
import { Markdown, VisualBlock } from '@blerglab/chat'
import MessageViewer from '../../../../packages/chat/src/components/MessageViewer'
import CodeViewer from '../../../../packages/chat/src/components/CodeViewer'
import '@blerglab/chat/blerg.css'
import '../index.css'

const report = '# Release report\n\n| Team | Status | Total |\n| --- | --- | --- |\n| **Design** | Ready | 42 |\n| Formula | =1+2 | 0 |\n\n```typescript\nconst total = 42\n```'
afterEach(() => {
  vi.restoreAllMocks()
  document.querySelector('iframe[data-content-print]')?.remove()
  document.documentElement.removeAttribute('data-theme')
})
for (const width of [390, 3840]) it(`opens inline exports by keyboard without changing initial geometry at ${width}px`, async () => {
  await page.viewport(width, width === 390 ? 844 : 2160)
  render(<div style={{ width: Math.min(width - 24, 640) }}><Markdown text={report} /></div>)
  const trigger = await screen.findByRole('button', { name: 'Table actions' })
  const table = screen.getByRole('table').parentElement!
  const height = table.getBoundingClientRect().height
  expect(screen.queryByRole('button', { name: 'Download CSV' })).toBeNull()
  expect(table.querySelector('svg')).toBeNull()
  expect(table.querySelector('.md-export-actions')!.getBoundingClientRect().height).toBe(34)
  trigger.focus()
  expect(trigger).toHaveFocus()
  expect(getComputedStyle(trigger.parentElement!).opacity).toBe('1')
  await userEvent.keyboard('{Enter}')
  const csv = screen.getByRole('button', { name: 'Download CSV' })
  expect(csv).toBeVisible()
  expect(table.getBoundingClientRect().height).toBe(height)
  await userEvent.tab()
  expect(screen.getByRole('button', { name: 'Print' })).toHaveFocus()
  await userEvent.tab()
  expect(csv).toHaveFocus()
  const escaped = vi.fn()
  const outside = (event: KeyboardEvent) => { if (event.key === 'Escape') escaped() }
  window.addEventListener('keydown', outside)
  try { await userEvent.keyboard('{Escape}'); expect(escaped).not.toHaveBeenCalled() }
  finally { window.removeEventListener('keydown', outside) }
  expect(trigger).toHaveFocus()
  expect(trigger).toHaveAttribute('aria-expanded', 'false')
  expect(screen.queryByRole('button', { name: 'Download CSV' })).toBeNull()
  expect(table.getBoundingClientRect().height).toBe(height)
})
for (const width of [390, 3840]) for (const theme of ['dark', 'colorblind-dark']) {
  it(`exports and prints message content at ${width}px in ${theme}`, async () => {
    await page.viewport(width, width === 390 ? 844 : 2160)
    document.documentElement.setAttribute('data-theme', theme)
    render(<div style={{ height: '100vh', display: 'flex', width: '100%' }}><MessageViewer text={report} label="Release report" embedded onClose={() => {}} /></div>)
    const toolbar = screen.getByRole('toolbar', { name: 'File actions' })
    await userEvent.click(screen.getByRole('button', { name: 'Table actions' }))
    const csv = screen.getByRole('button', { name: 'Download CSV' })
    expect(csv).toHaveTextContent('CSV')
    expect(screen.getByRole('toolbar', { name: 'Table export actions' }).querySelector('[aria-label="Download Markdown"]')).toHaveTextContent('MD')
    for (const button of document.querySelectorAll<HTMLButtonElement>('.md-table-wrap .viewer-action-labeled')) {
      expect(getComputedStyle(button).borderWidth).toBe('0px')
      const label = button.querySelector('span')!.getBoundingClientRect()
      const box = button.getBoundingClientRect()
      expect(label.left).toBeGreaterThanOrEqual(box.left)
      expect(label.right).toBeLessThanOrEqual(box.right)
      expect(box.right).toBeLessThanOrEqual(width)
    }
    csv.focus()
    expect(getComputedStyle(csv.closest('.md-export-actions')!).opacity).toBe('1')
    const saved: Blob[] = []
    const create = URL.createObjectURL.bind(URL)
    vi.spyOn(URL, 'createObjectURL').mockImplementation(blob => { saved.push(blob as Blob); return create(blob) })
    await userEvent.click(csv)
    await waitFor(() => expect(saved).toHaveLength(1))
    expect(await saved[0].text()).toContain('"Formula","\'=1+2","0"')
    await userEvent.click(within(toolbar).getByRole('button', { name: 'Download Markdown' }))
    await waitFor(() => expect(saved).toHaveLength(2))
    expect(await saved[1].text()).toBe(report)
    const trigger = screen.getByRole('button', { name: 'Table actions' })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    await userEvent.click(trigger)
    csv.focus()
    expect(csv).toHaveFocus()
    await commands.contentExportScreenshot(width, theme)
    // Keep the local print frame for inspection; suppress only the native dialog.
    const intercept = (event: Event) => {
      const frame = event.target
      if (frame instanceof HTMLIFrameElement && frame.hasAttribute('data-content-print')) frame.contentWindow!.print = () => {}
    }
    document.addEventListener('load', intercept, true)
    try {
      await userEvent.click(within(toolbar).getByRole('button', { name: 'Print' }))
      await waitFor(() => expect(document.querySelector<HTMLIFrameElement>('iframe[data-content-print]')?.contentDocument?.querySelector('table')).not.toBeNull())
      const printed = document.querySelector<HTMLIFrameElement>('iframe[data-content-print]')!.contentDocument!
      expect(printed.querySelector('button,[role=toolbar]')).toBeNull()
      expect(printed.defaultView!.getComputedStyle(printed.body).backgroundColor).toBe('rgb(255, 255, 255)')
      await commands.contentExportScreenshot(width, theme, true)
    } finally { document.removeEventListener('load', intercept, true) }
  })
}
it('keeps code export actions reachable in a pop-out', async () => {
  render(<CodeViewer text="const total = 42" lang="typescript" onClose={() => {}} />)
  expect(screen.getByRole('button', { name: 'Print' })).toHaveAttribute('title', 'Print')
  expect(screen.getByRole('button', { name: 'Download' })).toHaveAttribute('title', 'Download')
})
it('offers inline table exports and current SVG/PNG exports beside a diagram', async () => {
  render(<div style={{ width: 640 }}><Markdown text={report} /><VisualBlock kind="mermaid" source="flowchart LR; Draft-->Ready" /></div>)
  await userEvent.click(screen.getByRole('button', { name: 'Table actions' }))
  expect(screen.getByRole('button', { name: 'Download CSV' })).toBeInTheDocument()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Download SVG' })).toBeEnabled(), { timeout: 15000 })
  expect(screen.getByRole('button', { name: 'Download PNG' })).toBeEnabled()
})
