import { afterAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderMermaid } from './mermaid'

const api = vi.hoisted(() => ({initialize: vi.fn(), render: vi.fn()}))
vi.mock('mermaid', () => ({default: api}))
const originalDecode = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'decode')
const decode = vi.fn<() => Promise<void>>()
Object.defineProperty(HTMLImageElement.prototype, 'decode', {configurable: true, value: decode})
afterAll(() => {
  if (originalDecode) Object.defineProperty(HTMLImageElement.prototype, 'decode', originalDecode)
  else delete (HTMLImageElement.prototype as Partial<HTMLImageElement>).decode
})
beforeEach(() => {
  api.initialize.mockReset()
  api.render.mockReset().mockResolvedValue({svg: '<svg xmlns="http://www.w3.org/2000/svg"><text>Safe</text></svg>'})
  decode.mockReset().mockResolvedValue(undefined)
})

describe('Mermaid rendering lifetime and image errors', () => {
  it('uses fixed strict settings, decodes an inert image and cleans scratch DOM', async () => {
    const host = document.createElement('div')
    const dispose = await renderMermaid('flowchart LR; A-->B', host)
    expect(api.initialize).toHaveBeenCalledWith(expect.objectContaining({
      securityLevel: 'strict', startOnLoad: false, htmlLabels: false, suppressErrorRendering: true,
      secure: expect.arrayContaining(['securityLevel', 'htmlLabels', 'themeCSS']),
    }))
    expect(decode).toHaveBeenCalledOnce()
    expect(host.querySelector('img')?.src).toMatch(/^data:image\/svg\+xml/)
    expect(document.querySelector('[aria-hidden="true"]')).toBeNull()
    dispose()
    expect(host.children).toHaveLength(0)
  })
  it('propagates image decode failures to the component fallback and cleans scratch DOM', async () => {
    decode.mockRejectedValue(new Error('image blocked'))
    const host = document.createElement('div')
    await expect(renderMermaid('flowchart LR; A-->B', host)).rejects.toThrow('image blocked')
    expect(host.children).toHaveLength(0)
    expect(document.querySelector('[aria-hidden="true"]')).toBeNull()
  })
  it('preserves bounded SVG intrinsic dimensions instead of the image 300px fallback', async () => {
    api.render.mockResolvedValueOnce({svg: '<svg xmlns="http://www.w3.org/2000/svg" width="100%" viewBox="0 0 800 120"><text>Readable</text></svg>'})
    const host = document.createElement('div')
    const dispose = await renderMermaid('flowchart LR; A-->B', host)
    const svg = new DOMParser().parseFromString(decodeURIComponent(host.querySelector('img')!.src.split(',')[1]), 'image/svg+xml').documentElement
    expect(svg.getAttribute('width')).toBe('800')
    expect(svg.getAttribute('height')).toBe('120')
    dispose()
  })
  it('rejects hostile source before touching the renderer', async () => {
    await expect(renderMermaid('%%{init:{"securityLevel":"loose"}}%%\ngraph LR; A-->B', document.createElement('div'))).rejects.toThrow()
    expect(api.initialize).not.toHaveBeenCalled()
    expect(api.render).not.toHaveBeenCalled()
  })
  it('uses the resolved host tokens with a strict base theme', async () => {
    const host = document.createElement('div')
    host.style.setProperty('--chat-fg', '#eeeeee')
    host.style.setProperty('--chat-surface-2', '#222222')
    document.body.append(host)
    try {
      const dispose = await renderMermaid('flowchart LR; A-->B', host)
      expect(api.initialize).toHaveBeenCalledWith(expect.objectContaining({
        theme: 'base', themeVariables: expect.objectContaining({
          primaryTextColor: '#eeeeee', primaryColor: '#222222', background: 'transparent',
        }), secure: expect.arrayContaining(['theme', 'themeVariables']),
      }))
      dispose()
    } finally { host.remove() }
  })
  it('serializes global configuration with rendering and recovers after failure', async () => {
    let rejectFirst!: (reason: Error) => void
    api.render.mockImplementationOnce(() => new Promise((_resolve, reject) => { rejectFirst = reject }))
    const first = renderMermaid('flowchart LR; A-->B', document.createElement('div'))
    // Attach the rejection handler before allowing the failing queued job to run.
    const rejected = expect(first).rejects.toThrow('first failed')
    const second = renderMermaid('flowchart LR; C-->D', document.createElement('div'))
    try {
      await vi.waitFor(() => expect(api.render).toHaveBeenCalledTimes(1), { timeout: 15000 })
      expect(api.initialize).toHaveBeenCalledTimes(1)
    } finally {
      rejectFirst(new Error('first failed'))
      await rejected
    }
    const dispose = await second
    expect(api.initialize).toHaveBeenCalledTimes(2)
    expect(api.render).toHaveBeenCalledTimes(2)
    dispose()
  })

})
