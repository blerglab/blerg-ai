import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import type { BrowserCommandContext } from 'vitest/node'

// Element screenshots omit the viewport's scrollbar gutter. Capture the real iframe
// viewport, including that gutter, without resizing or changing the rendered app.
export async function questionScreenshot({ page, iframe }: BrowserCommandContext, width: number) {
  const directory = process.env.VITE_QUESTION_SHOT_DIR
  if (!directory || ![390, 3840].includes(width)) throw new Error('Invalid question screenshot')
  const box = await iframe.locator('html').boundingBox()
  if (!box) throw new Error('Missing question viewport')
  await mkdir(directory, { recursive: true })
  await page.screenshot({ path: resolve(directory, `question-stopped-waiting-${width}.png`), clip: { x: box.x, y: box.y, width, height: 1000 } })
}
