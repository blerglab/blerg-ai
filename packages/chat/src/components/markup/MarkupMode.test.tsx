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
  vi.spyOn(HTMLCanvasElement.prototype, 'getBoundingClientRect').mockReturnValue(STAGE as DOMRect)
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('MarkupMode', () => {
  it('renders the toolbar and the image', () => {
    setup()
    for (const name of ['Pen', 'Arrow', 'Box', 'Pin', 'Undo', 'Clear']) {
      expect(screen.getByRole('button', { name })).toBeInTheDocument()
    }
    expect(screen.getByRole('img', { name: 'screenshot-3.png' })).toHaveAttribute('src', 'blob:x')
    expect(screen.getByRole('button', { name: 'Pen' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  })

  it('a pin click adds pin 1 at that fraction of the image, with a note box', () => {
    const { canvas } = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Pin' }))
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
    fireEvent.click(screen.getByRole('button', { name: 'Pin' }))
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
    fireEvent.click(screen.getByRole('button', { name: 'Pin' }))
    press(canvas, 100, 150)
    fireEvent.click(within(screen.getByTestId('markup-pin-1')).getByRole('button', { name: 'Remove pin 1' }))
    expect(screen.queryByTestId('markup-pin-1')).toBeNull()
    expect(screen.getByRole('button', { name: 'Send' })).toBeDisabled()
  })

  it('Send is disabled with nothing drawn and enabled after a pin', () => {
    const { canvas } = setup()
    const send = screen.getByRole('button', { name: 'Send' })
    expect(send).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Pin' }))
    press(canvas, 10, 10)
    expect(send).toBeEnabled()
  })

  it('Send flattens to <stem>.annotated.png and passes the pins, then closes', async () => {
    vi.useFakeTimers()
    const { canvas, onSend, onClose } = setup()
    fireEvent.click(screen.getByRole('button', { name: 'Pin' }))
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
    fireEvent.click(screen.getByRole('button', { name: 'Pin' }))
    press(canvas, 200, 150)
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await screen.findByText('Not sent — not connected')
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByRole('button', { name: 'Send' })).toBeEnabled()
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
    expect(screen.getByRole('button', { name: 'Pin' })).toHaveAttribute('aria-pressed', 'true')
    press(canvas, 100, 150)
    // Typing in the note box must not switch tools.
    fireEvent.keyDown(screen.getByRole('textbox', { name: 'Note for pin 1' }), { key: 'a' })
    expect(screen.getByRole('button', { name: 'Pin' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.keyDown(root, { key: 'z' })
    expect(screen.queryByTestId('markup-pin-1')).toBeNull()
    fireEvent.keyDown(root, { key: 'p' })
    expect(screen.getByRole('button', { name: 'Pen' })).toHaveAttribute('aria-pressed', 'true')
  })
})
