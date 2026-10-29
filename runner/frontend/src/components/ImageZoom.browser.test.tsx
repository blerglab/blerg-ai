import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, waitFor, within } from '@testing-library/react'
import { page, userEvent } from 'vitest/browser'
import { ArtifactViewer, ChatContext, type ChatContextValue, type ArtifactInfo, type TransportFiles } from '@blerglab/chat'
import '@blerglab/chat/blerg.css'

const artifact: ArtifactInfo = { id: 'image', version: 1, name: 'landscape.svg', size: 500, view: 'image', content_type: 'image/svg+xml' }
const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="2400" height="1800"><rect width="2400" height="1800" fill="#20384b"/><path d="M0 1600L600 300L1200 1100L1800 200L2400 1600" fill="#4b8e78"/><circle cx="1800" cy="300" r="160" fill="#edc36b"/><path d="M1200 0V1800M0 900H2400" stroke="#fff" stroke-width="4"/></svg>'
const files: TransportFiles = { list: async () => [], raw: async () => new Blob([svg], { type: 'image/svg+xml' }), download: async () => {}, remove: async () => {}, upload: async () => { throw new Error('unused') } }
const shots = import.meta.env.VITE_IMAGE_ZOOM_SHOTS_DIR
function image(viewer: HTMLElement) { return viewer.querySelector('img')! }
function viewport(viewer: HTMLElement) { return within(viewer).getByRole('region', { name: /image viewport/ }) }
function scale(viewer: HTMLElement) { return image(viewer).getBoundingClientRect().width / 2400 }
function button(viewer: HTMLElement, name: string) { return within(viewer).getByRole('button', { name }) }
function wheel(el: HTMLElement, options: WheelEventInit) { fireEvent(el, new WheelEvent('wheel', { bubbles: true, cancelable: true, ...options })) }
async function setup(width: number) {
  await page.viewport(width, width === 3840 ? 2160 : 844)
  const body = (a: ArtifactInfo) => <div style={{ height: '100vh', display: 'flex', minWidth: 0 }}><ArtifactViewer embedded sessionId="zoom" files={files} artifact={a} onClose={() => {}} /></div>
  const rendered = render(body(artifact))
  const viewer = await rendered.findByRole('region', { name: artifact.name })
  await waitFor(() => expect(image(viewer)?.naturalWidth).toBe(2400))
  await waitFor(() => expect((button(viewer, 'Fit') as HTMLButtonElement).disabled).toBe(false))
  return { ...rendered, rerenderArtifact: (a: ArtifactInfo) => rendered.rerender(body(a)), viewer, el: viewport(viewer) }
}
afterEach(async () => { vi.restoreAllMocks(); await page.viewport(1280, 800) })

describe('image preview zoom and pan', () => {
  for (const width of [390, 1024, 3840]) {
    it(`uses every toolbar/key control, bounds and local pans at ${width}px`, async () => {
      const { viewer, el } = await setup(width)
      const fit = scale(viewer)
      expect(image(viewer).getBoundingClientRect().height).toBeLessThanOrEqual(el.clientHeight + 1)
      expect(image(viewer).getBoundingClientRect().width).toBeLessThanOrEqual(el.clientWidth + 1)
      await userEvent.click(button(viewer, 'Zoom in')); expect(scale(viewer)).toBeCloseTo(fit * 1.25)
      await userEvent.click(button(viewer, 'Zoom out')); expect(scale(viewer)).toBeCloseTo(fit)
      await userEvent.click(button(viewer, '100%')); expect(scale(viewer)).toBeCloseTo(1)
      expect(within(viewer).getByLabelText('Zoom percent').textContent).toBe('100%')
      await userEvent.click(button(viewer, 'Fit')); expect(scale(viewer)).toBeCloseTo(fit)
      await userEvent.dblClick(el); expect(scale(viewer)).toBeCloseTo(1)
      await userEvent.dblClick(el); expect(scale(viewer)).toBeCloseTo(fit)
      viewer.focus()
      await userEvent.keyboard('1'); expect(scale(viewer)).toBeCloseTo(1)
      await userEvent.keyboard('+'); expect(scale(viewer)).toBeCloseTo(1.25)
      await userEvent.keyboard('-'); expect(scale(viewer)).toBeCloseTo(1)
      await userEvent.keyboard('0'); expect(scale(viewer)).toBeCloseTo(fit)
      // At 200%, both axes overflow at every viewport, including 4K.
      for (let i = 0; i < 12; i++) fireEvent.keyDown(el, { key: '+' })
      const img = image(viewer), before = img.getBoundingClientRect()
      await userEvent.keyboard('{ArrowRight}{ArrowDown}')
      expect(img.getBoundingClientRect().left).toBeCloseTo(before.left - 40)
      expect(img.getBoundingClientRect().top).toBeCloseTo(before.top - 40)
      await userEvent.keyboard('{ArrowLeft}{ArrowUp}')
      expect(img.getBoundingClientRect().left).toBeCloseTo(before.left)
      expect(img.getBoundingClientRect().top).toBeCloseTo(before.top)
      await userEvent.wheel(el, { delta: { x: 60, y: 70 } })
      await waitFor(() => expect(img.getBoundingClientRect().left).toBeCloseTo(before.left - 60))
      expect(img.getBoundingClientRect().top).toBeCloseTo(before.top - 70)
      await userEvent.dragAndDrop(el, el, { sourcePosition: { x: 180, y: 200 }, targetPosition: { x: 210, y: 220 } })
      expect(img.getBoundingClientRect().left).toBeCloseTo(before.left - 30, 1)
      expect(img.getBoundingClientRect().top).toBeCloseTo(before.top - 50, 1)
      expect(getComputedStyle(el).cursor).toBe('grab')
      wheel(el, { deltaY: -100000, ctrlKey: true }); expect(scale(viewer)).toBeCloseTo(16)
      expect((button(viewer, 'Zoom in') as HTMLButtonElement).disabled).toBe(true)
      wheel(el, { deltaY: 100000, metaKey: true }); expect(scale(viewer)).toBeCloseTo(.1)
      expect((button(viewer, 'Zoom out') as HTMLButtonElement).disabled).toBe(true)
      fireEvent.keyDown(el, { key: '0' })
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(innerWidth)
      for (const control of viewer.querySelectorAll('.image-view-toolbar button,output')) {
        expect(control.getBoundingClientRect().right).toBeLessThanOrEqual(innerWidth)
      }
      if (shots) await page.screenshot({ path: `${shots}/image-fit-${width}.png` })
      fireEvent.keyDown(el, { key: '1' }); fireEvent.keyDown(el, { key: '+' })
      if (shots) await page.screenshot({ path: `${shots}/image-zoom-${width}.png` })
    })
  }

  it.each([390, 1024, 3840])('keeps the image point under the pointer during Ctrl/Cmd wheel and trackpad pinch at %ipx', async width => {
    const { viewer, el } = await setup(width)
    await userEvent.click(button(viewer, '100%'))
    const rect = el.getBoundingClientRect(), x = Math.round(rect.left + rect.width * .55), y = Math.round(rect.top + rect.height * (width === 3840 ? .5 : .4))
    const before = image(viewer).getBoundingClientRect(), oldScale = scale(viewer)
    const point = { x: (x - before.left) / oldScale, y: (y - before.top) / oldScale }
    for (const modifier of [{ ctrlKey: true }, { metaKey: true }, { ctrlKey: true }]) {
      wheel(el, { deltaY: -12.5, clientX: x, clientY: y, ...modifier })
      const after = image(viewer).getBoundingClientRect()
      expect((x - after.left) / scale(viewer)).toBeCloseTo(point.x)
      expect((y - after.top) / scale(viewer)).toBeCloseTo(point.y)
    }
    if (width === 3840) {
      // Manual zoom may still fit horizontally; keep its offset and allow movement in that space.
      expect(image(viewer).getBoundingClientRect().width).toBeLessThan(el.clientWidth)
      const left = image(viewer).getBoundingClientRect().left
      fireEvent.keyDown(el, { key: 'ArrowLeft' })
      expect(image(viewer).getBoundingClientRect().left).toBeCloseTo(left + 40)
      fireEvent.keyDown(el, { key: 'ArrowRight' })
      expect(image(viewer).getBoundingClientRect().left).toBeCloseTo(left)
    }
  })

  it('pinches and drags on touch, cleans up canceled gestures and clamps panning', async () => {
    const { viewer, el } = await setup(390)
    await userEvent.click(button(viewer, '100%'))
    vi.spyOn(el, 'setPointerCapture').mockImplementation(() => {})
    const rect = el.getBoundingClientRect(), left = rect.left, top = rect.top
    const a = { pointerId: 11, pointerType: 'touch', clientX: left + 100, clientY: top + 200 }
    const b = { pointerId: 12, pointerType: 'touch', clientX: left + 200, clientY: top + 200 }
    const before = image(viewer).getBoundingClientRect()
    fireEvent.pointerDown(el, a); fireEvent.pointerDown(el, b)
    expect(getComputedStyle(el).cursor).toBe('grabbing')
    fireEvent.pointerMove(el, { ...b, clientX: left + 300 })
    expect(scale(viewer)).toBeCloseTo(2)
    // Old midpoint 150 moves to 200; the same image point stays under it.
    expect((left + 200 - image(viewer).getBoundingClientRect().left) / 2).toBeCloseTo(left + 150 - before.left)
    fireEvent.pointerUp(el, b)
    const moved = image(viewer).getBoundingClientRect()
    fireEvent.pointerMove(el, { ...a, clientX: a.clientX + 25, clientY: a.clientY + 35 })
    expect(image(viewer).getBoundingClientRect().left).toBeCloseTo(moved.left + 25)
    expect(image(viewer).getBoundingClientRect().top).toBeCloseTo(moved.top + 35)
    fireEvent.pointerCancel(el, a)
    expect(getComputedStyle(el).cursor).toBe('grab')
    wheel(el, { deltaX: 1e6, deltaY: 1e6 })
    expect(image(viewer).getBoundingClientRect().right).toBeCloseTo(rect.right)
    expect(image(viewer).getBoundingClientRect().bottom).toBeCloseTo(rect.bottom)
    wheel(el, { deltaX: -1e6, deltaY: -1e6 })
    expect(image(viewer).getBoundingClientRect().left).toBeCloseTo(rect.left)
    expect(image(viewer).getBoundingClientRect().top).toBeCloseTo(rect.top)
  })

  it.each([390, 1024])('leaves wheel and touch scrolling to the page while fitted and claims them once zoomed at %ipx', async width => {
    const { viewer, el } = await setup(width)
    const fitted = image(viewer).getBoundingClientRect()
    const bubbled = vi.fn(); viewer.parentElement!.addEventListener('wheel', bubbled)
    const plain = new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaX: 30, deltaY: 50 })
    el.dispatchEvent(plain)
    expect(plain.defaultPrevented).toBe(false); expect(bubbled).toHaveBeenCalledTimes(1)
    expect(image(viewer).getBoundingClientRect().toJSON()).toEqual(fitted.toJSON())
    const zoomEvent = new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: -10, ctrlKey: true })
    el.dispatchEvent(zoomEvent); expect(zoomEvent.defaultPrevented).toBe(true)
    expect(getComputedStyle(el).touchAction).toBe('pan-x pan-y')
    await userEvent.click(button(viewer, '100%'))
    const zoomed = new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 20 })
    el.dispatchEvent(zoomed); expect(zoomed.defaultPrevented).toBe(true)
    expect(bubbled).toHaveBeenCalledTimes(1)
    expect(getComputedStyle(el).touchAction).toBe('none')
  })

  it('Fit shows a very tall image whole in a short group, below the 10% zoom floor, and refits on resize', async () => {
    await page.viewport(1024, 768)
    const tall = '<svg xmlns="http://www.w3.org/2000/svg" width="400" height="3000"><rect width="400" height="3000" fill="#345"/></svg>'
    const tallFiles: TransportFiles = { ...files, raw: async () => new Blob([tall], { type: 'image/svg+xml' }) }
    const body = (h: number) => <div style={{ height: h, display: 'flex', minWidth: 0 }}><ArtifactViewer embedded sessionId="tall" files={tallFiles} artifact={{ ...artifact, id: 'tall' }} onClose={() => {}} /></div>
    const rendered = render(body(260))
    const viewer = await rendered.findByRole('region', { name: artifact.name })
    await waitFor(() => expect((button(viewer, 'Fit') as HTMLButtonElement).disabled).toBe(false))
    const el = viewport(viewer)
    const inside = () => { const i = image(viewer).getBoundingClientRect(), v = el.getBoundingClientRect(); return i.top >= v.top - 1 && i.bottom <= v.bottom + 1 }
    expect(inside()).toBe(true); expect(scale(viewer)).toBeLessThan(.1)
    expect((button(viewer, 'Zoom out') as HTMLButtonElement).disabled).toBe(true)
    rendered.rerender(body(500))
    await waitFor(() => expect(image(viewer).getBoundingClientRect().height).toBeGreaterThan(300))
    expect(inside()).toBe(true)
  })

  it('does not suppress a fitted press, but still pinches from fit', async () => {
    const { viewer, el } = await setup(390)
    vi.spyOn(el, 'setPointerCapture').mockImplementation(() => {})
    const rect = el.getBoundingClientRect()
    const a = { pointerId: 21, pointerType: 'touch', clientX: rect.left + 100, clientY: rect.top + 200, bubbles: true, cancelable: true }
    const b = { ...a, pointerId: 22, clientX: rect.left + 200 }
    const down = new PointerEvent('pointerdown', a); el.dispatchEvent(down)
    expect(down.defaultPrevented).toBe(false); expect(el.classList.contains('is-dragging')).toBe(false)
    const mouse = new PointerEvent('pointerdown', { ...a, pointerId: 23, pointerType: 'mouse', button: 0 }); el.dispatchEvent(mouse)
    expect(mouse.defaultPrevented).toBe(false)
    const fit = scale(viewer)
    fireEvent.pointerDown(el, b)
    fireEvent.pointerMove(el, { ...b, clientX: rect.left + 300 })
    expect(scale(viewer)).toBeCloseTo(fit * 2)
    fireEvent.pointerUp(el, a); fireEvent.pointerUp(el, b)
  })

  it('double-click Fit to 100% zooms about the clicked point', async () => {
    const { viewer, el } = await setup(1024)
    const rect = el.getBoundingClientRect(), x = rect.left + rect.width * .3, y = rect.top + rect.height * .6
    const before = image(viewer).getBoundingClientRect(), s0 = scale(viewer)
    const point = { x: (x - before.left) / s0, y: (y - before.top) / s0 }
    fireEvent.dblClick(el, { clientX: x, clientY: y })
    const after = image(viewer).getBoundingClientRect()
    expect(scale(viewer)).toBeCloseTo(1)
    expect(Math.abs((x - after.left) - point.x)).toBeLessThan(1); expect(Math.abs((y - after.top) - point.y)).toBeLessThan(1)
  })

  it('accepts shifted and AltGr layouts by key value', async () => {
    const { viewer, el } = await setup(1024)
    const fit = scale(viewer)
    // '+' commonly needs Shift; AltGr layouts report altKey+ctrlKey false on some platforms but key values stay.
    fireEvent.keyDown(el, { key: '+', code: 'Equal', shiftKey: true }); expect(scale(viewer)).toBeCloseTo(fit * 1.25)
    fireEvent.keyDown(el, { key: '1', code: 'Digit1', shiftKey: true }); expect(scale(viewer)).toBeCloseTo(1)
    fireEvent.keyDown(el, { key: '0', code: 'Numpad0', shiftKey: true }); expect(scale(viewer)).toBeCloseTo(fit)
    fireEvent.keyDown(el, { key: '+', code: 'BracketRight', shiftKey: false }); expect(scale(viewer)).toBeCloseTo(fit * 1.25)
    fireEvent.keyDown(el, { key: '1', code: 'KeyQ', shiftKey: true }); expect(scale(viewer)).toBeCloseTo(1)
    fireEvent.keyDown(el, { key: '0', code: 'KeyP' }); expect(scale(viewer)).toBeCloseTo(fit)
  })

  it.each([390, 1024, 3840])('resets to Fit on a file or version change and leaves unrelated keyboard targets alone at %ipx', async width => {
    const { viewer, el, rerenderArtifact } = await setup(width)
    fireEvent.keyDown(el, { key: '+' })
    for (const tag of ['input', 'textarea', 'select', 'div']) {
      const field = document.createElement(tag)
      if (tag === 'div') field.contentEditable = 'true'
      viewer.querySelector('.image-view')!.append(field)
      const before = scale(viewer)
      const event = new KeyboardEvent('keydown', { key: '+', bubbles: true, cancelable: true })
      act(() => { field.dispatchEvent(event) })
      expect(event.defaultPrevented).toBe(false); expect(scale(viewer)).toBe(before)
      field.remove()
    }
    const outside = document.createElement('textarea'); document.body.append(outside); outside.focus()
    await userEvent.keyboard('+10-'); expect(outside.value).toBe('+10-'); outside.remove()
    for (const next of [{ ...artifact, version: 2 }, { ...artifact, id: 'another', name: 'another.svg' }]) {
      rerenderArtifact(next)
      const nextViewer = document.querySelector<HTMLElement>('.artifact-viewer')!
      await waitFor(() => expect((button(nextViewer, 'Fit') as HTMLButtonElement).disabled).toBe(false))
      expect(button(nextViewer, 'Fit').getAttribute('aria-pressed')).toBe('true')
      expect(scale(nextViewer)).toBeLessThanOrEqual(1)
      expect(image(nextViewer).getBoundingClientRect().width).toBeLessThanOrEqual(viewport(nextViewer).clientWidth + 1)
      expect(image(nextViewer).getBoundingClientRect().height).toBeLessThanOrEqual(viewport(nextViewer).clientHeight + 1)
      fireEvent.keyDown(viewport(nextViewer), { key: '+' })
    }
  })

  it.each([390, 1024, 3840])('keeps mark-up pin placement accurate after leaving a zoomed preview at %ipx', async width => {
    await page.viewport(width, width === 3840 ? 2160 : 844)
    const submitFeedback = vi.fn(async () => false)
    const chat: ChatContextValue = { sessionId: 'zoom', files, view: vi.fn(), latestVersion: () => undefined, byName: () => undefined, submitFeedback, reviewFor: async () => null }
    const { getByRole, getByTestId } = render(<ChatContext.Provider value={chat}><div style={{ height: '100vh', display: 'flex' }}><ArtifactViewer sessionId="zoom" embedded files={files} artifact={artifact} onClose={() => {}} /></div></ChatContext.Provider>)
    const viewer = getByRole('region', { name: artifact.name })
    await waitFor(() => expect((button(viewer, 'Fit') as HTMLButtonElement).disabled).toBe(false))
    fireEvent.keyDown(viewport(viewer), { key: '1' }); fireEvent.keyDown(viewport(viewer), { key: '+' })
    expect(scale(viewer)).toBeCloseTo(1.25)
    await userEvent.click(getByRole('button', { name: 'Mark up' }))
    const canvas = getByTestId('markup-canvas')
    await waitFor(() => expect(canvas.getBoundingClientRect().width).toBeGreaterThan(100))
    expect(viewer.querySelector('.image-view')).toBeNull()
    await userEvent.click(getByRole('button', { name: 'Comment' }))
    const box = canvas.getBoundingClientRect()
    fireEvent.pointerDown(canvas, { pointerId: 1, button: 0, clientX: box.left + box.width * .25, clientY: box.top + box.height * .75 })
    const pin = getByTestId('markup-pin-1')
    expect(Number(pin.dataset.x)).toBeCloseTo(.25, 4); expect(Number(pin.dataset.y)).toBeCloseTo(.75, 4)
    await userEvent.fill(getByRole('textbox', { name: 'Note for pin 1' }), 'Move this detail')
    await userEvent.click(getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(submitFeedback).toHaveBeenCalledTimes(1))
    if (shots) await page.screenshot({ path: `${shots}/image-markup-${width}.png` })
  })
})

it('contains two independent split image views', async () => {
  await page.viewport(3840, 2160)
  const { container, getAllByRole } = render(<div style={{ height: '100vh', display: 'flex', overflow: 'hidden' }}>
    {[0, 1].map(n => <div key={n} style={{ display: 'flex', width: '50%', minWidth: 0, minHeight: 0, overflow: 'hidden' }}><ArtifactViewer embedded sessionId="split" files={files} artifact={{ ...artifact, id: String(n) }} onClose={() => {}} /></div>)}
  </div>)
  const viewers = getAllByRole('region', { name: artifact.name })
  await waitFor(() => expect(viewers.every(v => !(button(v, 'Fit') as HTMLButtonElement).disabled)).toBe(true))
  const [first, second] = viewers as [HTMLElement, HTMLElement]
  const secondBefore = image(second).getBoundingClientRect()
  fireEvent.keyDown(viewport(first), { key: '1' }); fireEvent.keyDown(viewport(first), { key: '+' })
  wheel(viewport(first), { deltaX: 180, deltaY: 200 })
  expect(image(second).getBoundingClientRect().toJSON()).toEqual(secondBefore.toJSON())
  for (const el of container.querySelectorAll('.artifact-body,.embedded-viewer')) {
    expect(el.scrollHeight).toBe(el.clientHeight); expect(el.scrollWidth).toBe(el.clientWidth)
  }
  if (shots) await page.screenshot({ path: `${shots}/image-split-3840.png` })
})
