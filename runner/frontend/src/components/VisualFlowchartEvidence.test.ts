// BLG-468: proof that the published flowchart screenshots are a real 3840/1024/390 capture, not
// a stand-in page — the encoded PNG dimensions must equal the requested viewport
// (VisualFlowchart.browser.test.tsx's own `page.viewport` calls), same pattern as
// VisualReflowEvidence.test.ts.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { expect, it } from 'vitest'

const shots = process.env.VITE_VISUAL_FLOWCHART_SHOTS_DIR

it.skipIf(!shots).each([
  ['flowchart-chat-3840.png', 3840, 2160], ['flowchart-chat-1024.png', 1024, 768], ['flowchart-chat-390.png', 390, 844],
  ['flowchart-tab-3840.png', 3840, 2160], ['flowchart-tab-1024.png', 1024, 768], ['flowchart-tab-390.png', 390, 844],
])('encodes %s at its requested viewport', (name, width, height) => {
  const png = readFileSync(resolve(import.meta.dirname, shots!, name))
  expect(png.subarray(1, 4).toString()).toBe('PNG')
  expect(png.readUInt32BE(16)).toBe(width)
  expect(png.readUInt32BE(20)).toBe(height)
})
