import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import type { BrowserCommandContext } from 'vitest/node'

export async function layoutScreenshot({ page, iframe }: BrowserCommandContext, width: number, theme: string, surface: 'layouts' | 'layouts-saved' | 'workspace' = 'layouts') {
  const directory = process.env.VITE_TAB_SHOT_DIR
  if (!directory || ![390, 3840].includes(width) || !['light', 'dark', 'colorblind-dark'].includes(theme) || !['layouts', 'layouts-saved', 'workspace'].includes(surface)) throw new Error('Invalid layout screenshot')
  await page.setViewportSize({ width: 4200, height: 2400 })
  await iframe.locator('html').evaluate(() => {
    const container = window.frameElement?.parentElement
    if (container) { container.style.transform = 'scale(1)'; container.setAttribute('data-scale', '1') }
  })
  if (surface === 'layouts') await iframe.locator('.workspace-layout-list').evaluate(element => { element.scrollTop = 0 })
  if (surface === 'layouts-saved') await iframe.locator('.workspace-layout-list').evaluate(element => { element.scrollTop = element.scrollHeight })
  const box = await iframe.locator('html').boundingBox()
  if (!box) throw new Error('Missing layout viewport')
  await mkdir(directory, { recursive: true })
  await page.screenshot({ animations: 'disabled', path: resolve(directory, `${surface}-${theme}-${width}.png`), clip: { x: box.x, y: box.y, width, height: width === 390 ? 844 : 2160 } })
}
