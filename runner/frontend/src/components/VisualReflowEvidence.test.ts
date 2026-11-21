// BLG-467: the owner's review needs proof that the published screenshots are a real 3840/1024/390
// capture, not a stand-in page — the encoded PNG dimensions must equal the requested viewport
// (VisualReflow.browser.test.tsx's own `page.viewport` calls). Reads the files that test writes
// (gated on the same env var, same pattern as shellReviewEvidence.test.ts's L5).
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, it } from 'vitest'

const shots = process.env.VITE_VISUAL_REFLOW_SHOTS_DIR

it.skipIf(!shots).each([
  ['tab-chart-3840.png', 3840, 2160], ['tab-chart-1024.png', 1024, 768], ['tab-chart-390.png', 390, 844],
  ['tab-mermaid-3840.png', 3840, 2160], ['tab-mermaid-1024.png', 1024, 768], ['tab-mermaid-390.png', 390, 844],
])('encodes %s at its requested viewport', (name, width, height) => {
  const png = readFileSync(resolve(import.meta.dirname, shots!, name))
  expect(png.subarray(1, 4).toString()).toBe('PNG')
  expect(png.readUInt32BE(16)).toBe(width)
  expect(png.readUInt32BE(20)).toBe(height)
})
