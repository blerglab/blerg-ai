import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import MarkupMode from './MarkupMode'
import type { ArtifactInfo } from '../../transport/types'

// jsdom has no canvas 2D: every getContext returns a recording stub so a test can assert what a
// redraw drew (the overlay and the flattened copy share one drawing routine).
type Call = { name: string; args: unknown[] }
let calls: Call[] = []

function recordingContext(): CanvasRenderingContext2D {
  const target: Record<string, unknown> = {}
  return new Proxy(target, {
    get(t, prop: string) {
      if (prop in t) return t[prop]
      if (prop === 'measureText') return () => ({ width: 10 })
      return (...args: unknown[]) => {
        calls.push({ name: prop, args })
      }
    },
    set(t, prop: string, value) {
      t[prop] = value
      return true
    },
  }) as unknown as CanvasRenderingContext2D
}

const artifact: ArtifactInfo = {
  id: 'img1', name: 'screenshot-3.png', size: 1234, content_type: 'image/png', view: 'image',
}

// A 400×300 stage over an 800×600 image: one stage pixel is two image pixels.
const STAGE = { left: 0, top: 0, width: 400, height: 300, right: 400, bottom: 300, x: 0, y: 0, toJSON: () => ({}) }

function setup() {
  const onSend = vi.fn<(file: File, pins: unknown[]) => Promise<boolean>>().mockResolvedValue(true)
  const onClose = vi.fn()
  render(<MarkupMode artifact={artifact} src="blob:x" onSend={onSend} onClose={onClose} />)
  const img = screen.getByRole('img', { name: 'screenshot-3.png' }) as HTMLImageElement
  Object.defineProperty(img, 'naturalWidth', { value: 800, configurable: true })
  Object.defineProperty(img, 'naturalHeight', { value: 600, configurable: true })
  fireEvent.load(img)
  const canvas = screen.getByTestId('markup-canvas') as HTMLCanvasElement
  return { onSend, onClose, img, canvas }
}

function press(canvas: HTMLCanvasElement, x: number, y: number) {
  fireEvent.pointerDown(canvas, { clientX: x, clientY: y, pointerId: 1, button: 0, isPrimary: true })
}
function drag(canvas: HTMLCanvasElement, from: [number, number], to: [number, number]) {
  press(canvas, from[0], from[1])
  fireEvent.pointerMove(canvas, { clientX: to[0], clientY: to[1], pointerId: 1, isPrimary: true })
  fireEvent.pointerUp(canvas, { clientX: to[0], clientY: to[1], pointerId: 1, isPrimary: true })
}

beforeEach(() => {
  calls = []
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(() => recordingContext())
  vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (cb) {
    cb(new Blob(['png-bytes'], { type: 'image/png' }))
  })
  // The image's box is what maps the pointer; the canvas is laid over it.
  vi.spyOn(HTMLImageElement.prototype, 'getBoundingClientRect').mockReturnValue(STAGE as DOMRect)
  vi.spyOn(HTMLCanvasElement.prototype, 'getBoundingClientRect').mockReturnValue(STAGE as DOMRect)
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('MarkupMode', () => {
  it('renders the toolbar and the image', () => {
    setup()
    for (const name of ['Pen', 'Arrow', 'Box', 'Comment', 'Undo', 'Clear']) {
      expect(screen.getByRole('button', { name })).toBeInTheDocument()
    }
    expect(screen.getByRole('img', { name: 'screenshot-3.png' })).toHaveAttribute('src', 'blob:x')
    expect(screen.getByRole('button', { name: 'Pen' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  })

  it('a pin click adds pin 1 at that fraction of the image, with a note box', () => {
    const { canvas } = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
    press(canvas, 100, 150)
    const row = screen.getByTestId('markup-pin-1')
    expect(row).toHaveTextContent('1')
    expect(row).toHaveAttribute('data-x', '0.25')
    expect(row).toHaveAttribute('data-y', '0.5')
    const note = within(row).getByRole('textbox', { name: 'Note for pin 1' })
    expect(note).toHaveFocus()
  })

  it('typing a note updates the pin that is sent', async () => {
    const { canvas, onSend } = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
    press(canvas, 100, 150)
    press(canvas, 300, 30)
    fireEvent.change(screen.getByRole('textbox', { name: 'Note for pin 2' }), { target: { value: 'this button should be primary' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(onSend).toHaveBeenCalled())
    expect(onSend.mock.calls[0][1]).toEqual([
      { n: 1, note: '', x: 0.25, y: 0.5 },
      { n: 2, note: 'this button should be primary', x: 0.75, y: 0.1 },
    ])
  })

  it('a box drag adds a rectangle in image pixels that the redraw strokes', () => {
    const { canvas } = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Box' }))
    calls = []
    drag(canvas, [40, 30], [120, 90])
    const rects = calls.filter(c => c.name === 'strokeRect').map(c => c.args)
    expect(rects).toContainEqual([80, 60, 160, 120])
  })

  it('Undo removes the last mark', () => {
    const { canvas } = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Box' }))
    drag(canvas, [40, 30], [120, 90])
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled()
    calls = []
    fireEvent.click(screen.getByRole('button', { name: 'Undo' }))
    expect(calls.filter(c => c.name === 'strokeRect')).toHaveLength(0)
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  })

  it('removing a pin from the list takes it off the image', () => {
    const { canvas } = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
    press(canvas, 100, 150)
    fireEvent.click(within(screen.getByTestId('markup-pin-1')).getByRole('button', { name: 'Remove pin 1' }))
    expect(screen.queryByTestId('markup-pin-1')).toBeNull()
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  })

  it('Send is disabled with nothing drawn and enabled after a pin', () => {
    const { canvas } = setup()
    const send = screen.getByRole('button', { name: 'Send' })
    expect(send).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
    press(canvas, 10, 10)
    expect(send).toBeEnabled()
  })

  it('Send flattens to <stem>.annotated.png and passes the pins, then closes', async () => {
    vi.useFakeTimers()
    const { canvas, onSend, onClose } = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
    press(canvas, 200, 150)
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await act(async () => { await Promise.resolve() })
    await act(async () => { await Promise.resolve() })
    expect(onSend).toHaveBeenCalledTimes(1)
    const [file, pins] = onSend.mock.calls[0]
    expect(file).toBeInstanceOf(File)
    expect(file.name).toBe('screenshot-3.annotated.png')
    expect(file.type).toBe('image/png')
    expect(pins).toEqual([{ n: 1, note: '', x: 0.5, y: 0.5 }])
    // The flattened copy is drawn at the image's natural size, image first.
    expect(calls.some(c => c.name === 'drawImage')).toBe(true)
    expect(screen.getByText('Sent')).toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
    act(() => { vi.runAllTimers() })
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('says so inline when nothing was sent', async () => {
    const { canvas, onSend, onClose } = setup()
    onSend.mockResolvedValue(false)
    fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
    press(canvas, 200, 150)
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await screen.findByText('Not sent — not connected')
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled()
  })

  it('selects a drawing layer, edits its style and sends its attached comment at its location', async () => {
    const {canvas,onSend,img}=setup()
    fireEvent.click(screen.getByRole('button',{name:'Box'}))
    drag(canvas,[40,30],[120,90])
    const layer=screen.getByRole('button',{name:'Select Box annotation 1'})
    expect(layer).toHaveAttribute('aria-pressed','true')
    fireEvent.change(screen.getByRole('textbox',{name:'Comment for annotation 1'}),{target:{value:'Align this region'}})
    fireEvent.click(screen.getByRole('button',{name:'Danger color'}))
    fireEvent.click(screen.getByRole('button',{name:'Stroke 6'}))
    expect(screen.getByRole('button',{name:'Stroke 6'})).toHaveAttribute('aria-pressed','true')
    fireEvent.click(screen.getByRole('button',{name:'Send'}))
    await waitFor(()=>expect(onSend).toHaveBeenCalledTimes(1))
    expect(onSend.mock.calls[0][1]).toEqual([{n:1,note:'Align this region',x:0.2,y:0.2}])
    expect(img).toHaveAttribute('src','blob:x')
  })

  it.each(['Pen', 'Arrow'])('links a %s drawing comment to a numbered image position', async tool => {
    const {canvas,onSend}=setup()
    fireEvent.click(screen.getByRole('button',{name:tool}))
    drag(canvas,[40,30],[120,90])
    fireEvent.change(screen.getByRole('textbox',{name:'Comment for annotation 1'}),{target:{value:'Explain this mark'}})
    fireEvent.click(screen.getByRole('button',{name:'Send'}))
    await waitFor(()=>expect(onSend).toHaveBeenCalledTimes(1))
    expect(onSend.mock.calls[0][1]).toEqual([{n:1,note:'Explain this mark',x:0.3,y:0.3}])
  })

  it('hides a layer without deleting it and omits its drawing and comment from export', async () => {
    const {canvas,onSend}=setup()
    fireEvent.click(screen.getByRole('button',{name:'Box'}));drag(canvas,[40,30],[120,90])
    fireEvent.click(screen.getByRole('button',{name:'Hide annotation 1'}))
    expect(screen.getByRole('button',{name:'Show annotation 1'})).toBeInTheDocument()
    expect(screen.getByRole('button',{name:'Send'})).toBeDisabled()
    fireEvent.click(screen.getByRole('button',{name:'Comment'}));press(canvas,200,150)
    calls=[]
    fireEvent.click(screen.getByRole('button',{name:'Send'}))
    await waitFor(()=>expect(onSend).toHaveBeenCalledTimes(1))
    expect(calls.filter(c=>c.name==='strokeRect')).toHaveLength(0)
    expect(onSend.mock.calls[0][1]).toEqual([{n:2,note:'',x:0.5,y:0.5}])
  })

  it('deletes the selected layer and undo still removes the actual last remaining annotation', () => {
    const {canvas}=setup()
    fireEvent.click(screen.getByRole('button',{name:'Box'}));drag(canvas,[40,30],[120,90])
    fireEvent.click(screen.getByRole('button',{name:'Arrow'}));drag(canvas,[200,100],[300,200])
    fireEvent.click(screen.getByRole('button',{name:'Select Box annotation 1'}))
    fireEvent.click(screen.getByRole('button',{name:'Delete annotation 1'}))
    expect(screen.queryByRole('button',{name:'Select Box annotation 1'})).toBeNull()
    fireEvent.click(screen.getByRole('button',{name:'Undo'}))
    expect(screen.queryByRole('button',{name:'Select Arrow annotation 2'})).toBeNull()
    expect(screen.getByRole('button',{name:'Send'})).toBeDisabled()
  })

  it('selects the topmost visible drawing from the canvas in Select mode', () => {
    const {canvas}=setup()
    fireEvent.click(screen.getByRole('button',{name:'Box'}));drag(canvas,[40,30],[120,90])
    fireEvent.click(screen.getByRole('button',{name:'Arrow'}));drag(canvas,[200,100],[300,200])
    fireEvent.click(screen.getByRole('button',{name:'Select'}));press(canvas,40,60)
    expect(screen.getByRole('button',{name:'Select Box annotation 1'})).toHaveAttribute('aria-pressed','true')
    expect(screen.getByRole('button',{name:'Select Arrow annotation 2'})).toHaveAttribute('aria-pressed','false')
  })

  it('Esc closes the mode without reaching the dialog around it', () => {
    const { onClose } = setup()
    const outer = vi.fn()
    document.body.addEventListener('keydown', outer)
    fireEvent.keyDown(screen.getByTestId('markup-mode'), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(outer).not.toHaveBeenCalled()
    document.body.removeEventListener('keydown', outer)
  })

  it('letter keys pick tools and undo, except while typing a note', () => {
    const { canvas } = setup()
    const root = screen.getByTestId('markup-mode')
    fireEvent.keyDown(root, { key: 'b' })
    expect(screen.getByRole('button', { name: 'Box' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.keyDown(root, { key: 'n' })
    expect(screen.getByRole('button', { name: 'Comment' })).toHaveAttribute('aria-pressed', 'true')
    press(canvas, 100, 150)
    // Typing in the note box must not switch tools.
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Note for pin 1' }), { key: 'a' })
    expect(screen.getByRole('button', { name: 'Comment' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.keyDown(root, { key: 'z' })
    expect(screen.queryByTestId('markup-pin-1')).toBeNull()
    fireEvent.keyDown(root, { key: 'p' })
    expect(screen.getByRole('button', { name: 'Pen' })).toHaveAttribute('aria-pressed', 'true')
  })

  // Card #94: the stage is a grid item that stretches to its column, while the image is capped by
  // height and sits at its left. A canvas the size of the stage mapped the pointer against the
  // wrong box, so the marks landed left of where they were drawn and squashed sideways.
  describe('a stage wider than a height-capped image', () => {
    // The image shows at 400×300 at (100, 50); the stage (and the canvas, before the fix) is 900 wide.
    const IMAGE = { left: 100, top: 50, width: 400, height: 300, right: 500, bottom: 350, x: 100, y: 50, toJSON: () => ({}) }
    const WIDE = { left: 100, top: 50, width: 900, height: 300, right: 1000, bottom: 350, x: 100, y: 50, toJSON: () => ({}) }
    beforeEach(() => {
      vi.spyOn(HTMLImageElement.prototype, 'getBoundingClientRect').mockReturnValue(IMAGE as DOMRect)
      vi.spyOn(HTMLCanvasElement.prototype, 'getBoundingClientRect').mockReturnValue(WIDE as DOMRect)
      vi.spyOn(HTMLDivElement.prototype, 'getBoundingClientRect').mockReturnValue(WIDE as DOMRect)
    })

    it('maps a pin at the image center to the center, and places the canvas over the image only', () => {
      const { canvas } = setup()
      fireEvent.click(screen.getByRole('button', { name: 'Comment' }))
      press(canvas, 300, 200)
      const row = screen.getByTestId('markup-pin-1')
      expect(row).toHaveAttribute('data-x', '0.5')
      expect(row).toHaveAttribute('data-y', '0.5')
      expect(canvas.style.width).toBe('400px')
      expect(canvas.style.height).toBe('300px')
      expect(canvas.style.left).toBe('0px')
    })

    it('draws the overlay at one uniform scale and flattens the same natural pixels', async () => {
      const { canvas, onSend } = setup()
      fireEvent.click(screen.getByRole('button', { name: 'Box' }))
      calls = []
      drag(canvas, [200, 100], [400, 300]) // image pixels (200,100) to (600,500)
      const scales = calls.filter(c => c.name === 'scale').map(c => c.args)
      expect(scales.length).toBeGreaterThan(0)
      for (const [sx, sy] of scales) expect(sx).toBe(sy)
      expect(calls.filter(c => c.name === 'strokeRect').map(c => c.args)).toContainEqual([200, 100, 400, 400])
      fireEvent.change(screen.getByRole('textbox', { name: 'Comment for annotation 1' }), { target: { value: 'here' } })
      calls = []
      fireEvent.click(screen.getByRole('button', { name: 'Send' }))
      await waitFor(() => expect(onSend).toHaveBeenCalledTimes(1))
      // The flattened copy strokes the very same rectangle, in natural pixels, with no scale.
      expect(calls.filter(c => c.name === 'scale')).toHaveLength(0)
      expect(calls.filter(c => c.name === 'strokeRect').map(c => c.args)).toContainEqual([200, 100, 400, 400])
      expect(onSend.mock.calls[0][1]).toEqual([{ n: 1, note: 'here', x: 0.5, y: 0.5 }])
    })
  })
})
