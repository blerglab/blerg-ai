import { describe, expect, it, vi } from 'vitest'
import { render } from '@testing-library/react'
import { MarkupMode, PdfPages, type Anchor, type Pin } from '@blerglab/chat'

// The review of a PDF and the mark-up of an image both rest on things jsdom does not have: pdf.js
// with its worker and text layer, and a real 2D canvas. Their unit tests run against fakes; these
// run the real ones, in the browser the app runs in.

// A one-page PDF with one line of text. The xref table is left out on purpose: pdf.js rebuilds it.
const PDF = `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 400 200]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj
4 0 obj<</Length 64>>stream
BT /F1 18 Tf 20 120 Td (The mean rose by twelve percent) Tj ET
endstream endobj
5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj
trailer<</Root 1 0 R/Size 6>>
%%EOF`

async function until<T>(get: () => T | null | undefined | false, ms = 20_000): Promise<T> {
  const end = Date.now() + ms
  for (;;) {
    const v = get()
    if (v) return v
    if (Date.now() > end) throw new Error('timed out waiting')
    await new Promise(r => setTimeout(r, 50))
  }
}

function pane(): HTMLDivElement {
  const host = document.createElement('div')
  host.style.cssText = 'width:640px;height:480px;overflow:auto'
  document.body.appendChild(host)
  return host
}

describe('PDF review pages (real pdf.js)', () => {
  it('draws the page, lays a selectable text layer over it, and anchors a selection to the page', async () => {
    const onSelection = vi.fn<(a: Anchor | null) => void>()
    const { container } = render(
      <PdfPages blob={new Blob([PDF], { type: 'application/pdf' })} requests={[]} onHover={() => {}} onSelection={onSelection} />,
      { container: pane() },
    )
    const span = await until(() =>
      [...container.querySelectorAll<HTMLElement>('.textLayer span')].find(s => s.textContent?.includes('mean rose')))
    // The canvas was really painted, at the pane's width.
    const canvas = container.querySelector('canvas')!
    expect(canvas.width).toBeGreaterThan(300)
    const px = canvas.getContext('2d')!.getImageData(0, 0, canvas.width, canvas.height).data
    let dark = 0
    for (let i = 0; i < px.length; i += 4) if (px[i] < 100 && px[i + 3] > 0) dark++
    expect(dark).toBeGreaterThan(50) // glyphs, not a blank page

    const node = span.firstChild as Text
    const at = node.data.indexOf('mean rose')
    const range = document.createRange()
    range.setStart(node, at)
    range.setEnd(node, at + 'mean rose'.length)
    const sel = document.getSelection()!
    sel.removeAllRanges()
    sel.addRange(range)
    const anchor = await until(() => onSelection.mock.calls.map(c => c[0]).find(a => a?.quote === 'mean rose'))
    expect(anchor).toMatchObject({ kind: 'text', page: 1, occurrence: 1 })
  }, 30_000)

  it('highlights a request where its quote sits on the page', async () => {
    const { container } = render(
      <PdfPages
        blob={new Blob([PDF], { type: 'application/pdf' })}
        requests={[{ id: 'r1', anchor: { kind: 'text', quote: 'twelve percent', page: 1, occurrence: 1 }, text: 'say 12%', status: 'open', createdAt: '2026-10-06T00:00:00Z' }]}
        focusedId="r1"
        onHover={() => {}}
        onSelection={() => {}}
      />,
      { container: pane() },
    )
    const box = await until(() => container.querySelector<HTMLElement>('.pdf-hl[data-request="r1"]'))
    expect(box.classList.contains('open')).toBe(true)
    expect(box.classList.contains('focus')).toBe(true)
    expect(box.getBoundingClientRect().width).toBeGreaterThan(20)
  }, 30_000)
})

describe('Image mark-up (real canvas)', () => {
  it('flattens the image, a box and a numbered pin into a PNG at the image’s own size', async () => {
    // A 320×200 grey image to draw on.
    const c = document.createElement('canvas')
    c.width = 320
    c.height = 200
    const g = c.getContext('2d')!
    g.fillStyle = '#888'
    g.fillRect(0, 0, 320, 200)
    const src = c.toDataURL('image/png')

    let sent: { file: File; pins: Pin[] } | null = null
    const { container, getByRole } = render(
      <div style={{ width: 640 }}>
        <MarkupMode
          artifact={{ id: 'a1', name: 'shot.png', size: 100, content_type: 'image/png', view: 'image' }}
          src={src}
          onSend={async (file, pins) => { sent = { file, pins }; return true }}
          onClose={() => {}}
        />
      </div>,
    )
    const overlay = await until(() => {
      const el = container.querySelector<HTMLCanvasElement>('canvas')
      return el && el.getBoundingClientRect().width > 50 ? el : null
    })
    const r = overlay.getBoundingClientRect()
    const at = (fx: number, fy: number) => ({ clientX: r.left + r.width * fx, clientY: r.top + r.height * fy, pointerId: 1, bubbles: true })
    const tick = () => new Promise(res => setTimeout(res, 20))

    getByRole('button', { name: /box/i }).click()
    await tick()
    overlay.dispatchEvent(new PointerEvent('pointerdown', at(0.2, 0.2)))
    overlay.dispatchEvent(new PointerEvent('pointermove', at(0.6, 0.6)))
    overlay.dispatchEvent(new PointerEvent('pointerup', at(0.6, 0.6)))
    await tick()
    getByRole('button', { name: /^pin/i }).click()
    await tick()
    overlay.dispatchEvent(new PointerEvent('pointerdown', at(0.8, 0.25)))
    overlay.dispatchEvent(new PointerEvent('pointerup', at(0.8, 0.25)))

    const send = await until(() => {
      const b = getByRole('button', { name: /^send/i }) as HTMLButtonElement
      return b.disabled ? null : b
    })
    send.click()
    const got = await until(() => sent) as { file: File; pins: Pin[] }
    expect(got.file.name).toBe('shot.annotated.png')
    expect(got.file.type).toBe('image/png')
    expect(got.pins).toHaveLength(1)
    expect(got.pins[0].n).toBe(1)
    expect(got.pins[0].x).toBeGreaterThan(0.7)

    // The PNG is the image's own size and the marks are in it (pixels that are no longer grey).
    const bmp = await createImageBitmap(got.file)
    expect([bmp.width, bmp.height]).toEqual([320, 200])
    const out = document.createElement('canvas')
    out.width = 320
    out.height = 200
    const og = out.getContext('2d')!
    og.drawImage(bmp, 0, 0)
    const px = og.getImageData(0, 0, 320, 200).data
    let marked = 0
    for (let i = 0; i < px.length; i += 4) if (Math.abs(px[i] - 0x88) > 40 || Math.abs(px[i + 2] - 0x88) > 40) marked++
    expect(marked).toBeGreaterThan(200)
  }, 30_000)
})
