import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import type { BrowserCommandContext } from 'vitest/node'

export async function channelScreenshot({ page, iframe }: BrowserCommandContext, width: number, theme: string, viewer: string) {
  const directory = process.env.VITE_CHANNEL_SHOTS_DIR
  if (!directory || ![390, 1920, 3840].includes(width) || !['dark', 'colorblind-dark'].includes(theme) || !['deck', 'diff'].includes(viewer)) throw new Error('Invalid channel screenshot')
  await page.setViewportSize({ width: 4200, height: 2400 })
  await iframe.locator('html').evaluate(() => {
    const container = window.frameElement?.parentElement
    if (container) { container.style.transform = 'scale(1)'; container.setAttribute('data-scale', '1') }
  })
  const box = await iframe.locator('html').boundingBox()
  if (!box) throw new Error('Missing channel viewport')
  await mkdir(directory, { recursive: true })
  await page.screenshot({ animations: 'disabled', path: resolve(directory, `channel-${viewer}-${theme}-${width}.png`), clip: { x: box.x, y: box.y, width, height: width === 390 ? 844 : width === 1920 ? 1080 : 2160 } })
}
