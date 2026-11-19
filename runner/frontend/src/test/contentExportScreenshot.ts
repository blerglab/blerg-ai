import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import type { BrowserCommandContext } from 'vitest/node'

/** Captures the shipped viewer and the same local document passed to browser print. */
export async function contentExportScreenshot({ page, iframe, context }: BrowserCommandContext, width: number, theme: string, print = false) {
  if (![390, 3840].includes(width) || !['dark', 'colorblind-dark'].includes(theme)) throw new Error('Invalid export screenshot')
  const directory = resolve(process.cwd(), '../../.cache/blg589-shots')
  await mkdir(directory, { recursive: true })
  if (print) {
    const html = await iframe.locator('iframe[data-content-print]').getAttribute('srcdoc')
    if (!html) throw new Error('Print document is unavailable')
    const preview = await context.newPage()
    try {
      await preview.setViewportSize({ width, height: width === 390 ? 844 : 2160 })
      await preview.emulateMedia({ media: 'print' })
      await preview.setContent(html)
      await preview.screenshot({ path: resolve(directory, `print-${theme}-${width}.png`) })
    } finally { await preview.close() }
    return
  }
  await page.setViewportSize({ width: 4200, height: 2400 })
  await iframe.locator('html').evaluate(() => {
    const container = window.frameElement?.parentElement
    if (container) { container.style.transform = 'scale(1)'; container.setAttribute('data-scale', '1') }
  })
  const box = await iframe.locator('html').boundingBox()
  if (!box) throw new Error('Missing viewer viewport')
  await page.screenshot({ path: resolve(directory, `viewer-${theme}-${width}.png`), clip: { x: box.x, y: box.y, width, height: width === 390 ? 844 : 2160 } })
}
