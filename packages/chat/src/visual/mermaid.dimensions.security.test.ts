import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderMermaid } from './mermaid'

const api = vi.hoisted(() => ({initialize: vi.fn(), render: vi.fn()}))
// Mermaid itself runs in a sandboxed frame (mermaidFrame.ts, tested in Chromium); here the frame
// is a stand-in that hands the config and source to a mock Mermaid.
vi.mock('./mermaidFrame', () => ({renderInFrame: async (source: string, config: unknown) => {
  api.initialize(config)
  return (await api.render('blerg-diagram', source)).svg
}}))
const originalDecode = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'decode')
const decode = vi.fn<() => Promise<void>>()
Object.defineProperty(HTMLImageElement.prototype, 'decode', {configurable: true, value: decode})

afterAll(() => {
  if (originalDecode) Object.defineProperty(HTMLImageElement.prototype, 'decode', originalDecode)
  else delete (HTMLImageElement.prototype as Partial<HTMLImageElement>).decode
})

beforeEach(() => {
  api.initialize.mockReset()
  api.render.mockReset()
  decode.mockReset().mockResolvedValue(undefined)
})

function output(host: HTMLElement) {
  const image = host.querySelector('img')!
  return new DOMParser().parseFromString(decodeURIComponent(image.src.split(',')[1]), 'image/svg+xml')
}

describe('independent Mermaid intrinsic dimension security', () => {
  it.each([
    ['ordinary wide', '0 0 800 120', 800, 120],
    ['ordinary tall', '0 0 120 800', 120, 800],
    ['negative origin', '-20 -10 800 120', 800, 120],
    ['comma-separated bounds', '0,0,800,120', 800, 120],
    ['scientific notation', '0 0 8e2 1.2e2', 800, 120],
    ['large wide', '0 0 8192 4096', 4096, 2048],
    ['large tall', '0 0 4096 8192', 2048, 4096],
    ['extreme finite square', '0 0 1e308 1e308', 4096, 4096],
  ])('sets only finite bounded intrinsic sizes for %s SVG', async (_name, viewBox, expectedWidth, expectedHeight) => {
    api.render.mockResolvedValueOnce({svg: `<svg xmlns="http://www.w3.org/2000/svg" width="100%" viewBox="${viewBox}"><text>Safe</text></svg>`})
    const host = document.createElement('div')
    const dispose = await renderMermaid('flowchart LR; A-->B', host)
    const svg = output(host).documentElement
    const width = Number(svg.getAttribute('width'))
    const height = Number(svg.getAttribute('height'))
    expect(width).toBeCloseTo(expectedWidth as number)
    expect(height).toBeCloseTo(expectedHeight as number)
    expect(width).toBeGreaterThan(0)
    expect(height).toBeGreaterThan(0)
    expect(Math.max(width, height)).toBeLessThanOrEqual(4096)
    expect(svg.getAttribute('viewBox')).toBe(viewBox)
    expect(document.querySelector('[aria-hidden="true"]')).toBeNull()
    dispose()
    expect(host.childElementCount).toBe(0)
  })

  it.each(['0 0 1e309 120', '0 0 NaN 120', '0 0 -800 120', '0 0 800 0', '0 0 800', ''])('never writes dimensions derived from invalid bounds %s', async viewBox => {
    api.render.mockResolvedValueOnce({svg: `<svg xmlns="http://www.w3.org/2000/svg" width="100%" viewBox="${viewBox}"><text>Safe</text></svg>`})
    const host = document.createElement('div')
    const dispose = await renderMermaid('flowchart LR; A-->B', host)
    const svg = output(host).documentElement
    expect(svg.getAttribute('width')).toBe('100%')
    expect(svg.hasAttribute('height')).toBe(false)
    expect(svg.hasAttribute('onload')).toBe(false)
    dispose()
  })

  it('still rejects active SVG before sizing or decoding', async () => {
    api.render.mockResolvedValueOnce({svg: '<svg xmlns="http://www.w3.org/2000/svg" width="100%" viewBox="0 0 800 120" onload="alert(1)"><text>Unsafe</text></svg>'})
    const host = document.createElement('div')
    await expect(renderMermaid('flowchart LR; A-->B', host)).rejects.toThrow()
    expect(decode).not.toHaveBeenCalled()
    expect(host.childElementCount).toBe(0)
    expect(document.querySelector('[aria-hidden="true"]')).toBeNull()
  })
})
