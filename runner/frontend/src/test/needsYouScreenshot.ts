import { mkdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import type { BrowserCommandContext } from 'vitest/node'

// Keep the test iframe at native scale before capturing its complete viewport.
export async function needsYouScreenshot({ page, iframe }: BrowserCommandContext, width: number, theme: string, surface = 'needs-you') {
 const directory=process.env.VITE_BLG497_SHOTS_DIR
 if (!directory || ![390,3840].includes(width) || !['needs-you','panel','empty'].includes(surface) || !['light','dark','colorblind-dark'].includes(theme)) throw new Error('Invalid Needs you screenshot')
 await page.setViewportSize({ width: 4200, height: 2400 })
 await iframe.locator('html').evaluate(() => {
  const container=window.frameElement?.parentElement
  if (container) { container.style.transform='scale(1)'; container.setAttribute('data-scale','1') }
 })
 const box=await iframe.locator('html').boundingBox()
 if (!box) throw new Error('Missing Needs you viewport')
 await mkdir(directory,{ recursive: true })
 await page.screenshot({ path: resolve(directory,`${surface}-${theme}-${width}.png`), clip:{x:box.x,y:box.y,width,height:width===390?844:2160} })
}
