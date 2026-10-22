// Card #94: image mark-up must land where it was drawn. The mark-up stage stretches to its grid
// column while the image is capped by height (or is a portrait), so the stage is wider than the
// image. The canvas used to cover the whole stage and mapped the pointer and the redraw against
// that box, so the flattened PNG had its marks shifted left and squashed sideways. Here, in real
// Chromium with the real viewer layout: a mark drawn at a natural pixel shows over that pixel on
// screen, lands on that pixel in the flattened PNG, and a pin there reports that fraction.
import { afterEach, describe, expect, it } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { fireEvent } from '@testing-library/react'
import { ArtifactViewer, ChatContext, localStorageChatStorage, type ArtifactInfo, type ChatContextValue, type TransportFiles } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'
import '../index.css'

const GRAY = 136

async function png(w: number, h: number): Promise<Blob> {
  const canvas = document.createElement('canvas')
  canvas.width = w; canvas.height = h
  const ctx = canvas.getContext('2d')!
  ctx.fillStyle = '#888888'; ctx.fillRect(0, 0, w, h)
  return new Promise(resolve => canvas.toBlob(b => resolve(b!), 'image/png'))
}

type Box = { left: number; top: number; right: number; bottom: number }
/** The bounding box of every pixel that is not the plain background (gray, or transparent). */
function marked(data: Uint8ClampedArray, w: number, h: number, background: 'gray' | 'clear'): Box | null {
  let box: Box | null = null
  for (let y = 0; y < h; y++) for (let x = 0; x < w; x++) {
    const i = (y * w + x) * 4
    const plain = background === 'clear' ? data[i + 3] === 0 : data[i] === GRAY && data[i + 1] === GRAY && data[i + 2] === GRAY
    if (plain) continue
    if (!box) box = { left: x, top: y, right: x, bottom: y }
    else { box.left = Math.min(box.left, x); box.top = Math.min(box.top, y); box.right = Math.max(box.right, x); box.bottom = Math.max(box.bottom, y) }
  }
  return box
}
const center = (b: Box) => ({ x: (b.left + b.right) / 2, y: (b.top + b.bottom) / 2 })
const frame = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))

afterEach(async () => {
  localStorageChatStorage.set('blerg.chat.viewer.maximized.markup', null)
  await page.viewport(1280, 800)
})

const CASES = [
  // [viewport w, h, image w, h, maximize]
  [1920, 1080, 1600, 900, false], // the owner's case: x came out near 623 instead of 800
  [3840, 2160, 1600, 900, false],
  [3840, 2160, 1600, 900, true],
  [390, 844, 1600, 900, false],
  [1920, 1080, 600, 1200, false],
  [3840, 2160, 600, 1200, true],
  [390, 844, 600, 1200, false],
  [390, 844, 600, 1200, true],
] as const

describe('image mark-up lands where it was drawn', () => {
  it.each(CASES)('on a %i × %i viewport, a %i × %i image (maximized: %s)', async (vw, vh, iw, ih, maximize) => {
    await page.viewport(vw, vh)
    const blob = await png(iw, ih)
    const sent: File[] = []
    const files: TransportFiles = {
      list: async () => [], raw: async () => blob, download: async () => {}, remove: async () => {},
      upload: async () => { throw new Error('unused') },
    }
    const context: ChatContextValue = {
      sessionId: 'markup-place', files: null, view: () => {}, latestVersion: () => undefined, byName: () => undefined,
      reviewFor: async () => null,
      // Keep the editor open after Send so the pins can be checked too.
      submitFeedback: async (_text, attached) => { sent.push(...attached); return false },
    }
    const artifact: ArtifactInfo = { id: 'shot', name: 'shot.png', size: blob.size, content_type: 'image/png', view: 'image' }
    const view = render(
      <ChatContext.Provider value={context}>
        <ArtifactViewer sessionId="markup-place" files={files} artifact={artifact} onClose={() => {}} />
      </ChatContext.Provider>,
    )
    await userEvent.click(await view.findByRole('button', { name: 'Mark up' }))
    if (maximize) await userEvent.click(view.getByRole('button', { name: 'Maximize' }))
    const img = view.getByRole('img', { name: 'shot.png' }) as HTMLImageElement
    await waitFor(() => expect(img.naturalWidth).toBe(iw))
    const canvas = view.getByTestId('markup-canvas') as HTMLCanvasElement
    await frame()
    await waitFor(() => expect(Math.abs(canvas.getBoundingClientRect().width - img.getBoundingClientRect().width)).toBeLessThan(1))

    const ir = img.getBoundingClientRect()
    const sr = document.querySelector('.markup-stage')!.getBoundingClientRect()
    // The precondition the bug needs: a stage wider than the image. Beside the annotations column
    // (wider than a phone) the stage stretches to its column; on a phone the stacked stage already
    // hugs the image, and the placement checks below still hold.
    if (vw > 900) expect(sr.width - 2).toBeGreaterThan(ir.width + 20)
    // The canvas covers the image's pixels exactly, no more.
    const cr = canvas.getBoundingClientRect()
    for (const k of ['left', 'top', 'width', 'height'] as const) expect(Math.abs(cr[k] - ir[k])).toBeLessThan(1)

    // Draw a small box around the natural pixel at (50%, 50%).
    const scale = ir.width / iw
    // Measured at each event: a tool click can scroll the workspace on a phone.
    const at = (fx: number, fy: number, dx = 0, dy = 0) => {
      const r = img.getBoundingClientRect()
      return { clientX: r.left + fx * r.width + dx, clientY: r.top + fy * r.height + dy, pointerId: 1, button: 0, isPrimary: true }
    }
    await userEvent.click(view.getByRole('button', { name: 'Box' }))
    const d = 30
    fireEvent.pointerDown(canvas, at(0.5, 0.5, -d, -d))
    fireEvent.pointerMove(canvas, at(0.5, 0.5, d, d))
    fireEvent.pointerUp(canvas, at(0.5, 0.5, d, d))
    await frame()
    const tolerance = Math.max(4, 3 / scale) // a few screen pixels, in natural pixels

    // On screen: the overlay's marks are centered on the image's center.
    const overlay = marked(canvas.getContext('2d')!.getImageData(0, 0, canvas.width, canvas.height).data, canvas.width, canvas.height, 'clear')!
    expect(overlay).not.toBeNull()
    const k = canvas.width / iw
    expect(Math.abs(center(overlay).x / k - iw / 2)).toBeLessThan(tolerance)
    expect(Math.abs(center(overlay).y / k - ih / 2)).toBeLessThan(tolerance)
    // Uniform scale: the drawn box is as wide as it is tall (it was drawn square on screen).
    expect(Math.abs((overlay.right - overlay.left) - (overlay.bottom - overlay.top))).toBeLessThan(3)

    // In the flattened PNG: the same natural pixel, the same square.
    await userEvent.click(view.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(sent.length).toBe(1))
    const bitmap = await createImageBitmap(sent[0]!)
    expect([bitmap.width, bitmap.height]).toEqual([iw, ih])
    const out = document.createElement('canvas'); out.width = iw; out.height = ih
    const ctx = out.getContext('2d')!; ctx.drawImage(bitmap, 0, 0); bitmap.close()
    const flat = marked(ctx.getImageData(0, 0, iw, ih).data, iw, ih, 'gray')!
    expect(flat).not.toBeNull()
    expect(Math.abs(center(flat).x - iw / 2)).toBeLessThan(tolerance)
    expect(Math.abs(center(flat).y - ih / 2)).toBeLessThan(tolerance)
    // The box spans 2d screen pixels, which is 2d / scale natural pixels, in both directions.
    const span = (2 * d) / scale
    expect(Math.abs((flat.right - flat.left) - (flat.bottom - flat.top))).toBeLessThan(3)
    expect(flat.right - flat.left).toBeGreaterThanOrEqual(span - 1)

    // A pin at (25%, 75%) reports that fraction.
    await userEvent.click(view.getByRole('button', { name: 'Comment' }))
    fireEvent.pointerDown(canvas, at(0.25, 0.75))
    const pin = await view.findByTestId(/^markup-pin-/)
    expect(Number(pin.dataset.x)).toBeCloseTo(0.25, 2)
    expect(Number(pin.dataset.y)).toBeCloseTo(0.75, 2)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
    view.unmount()
  })
})
