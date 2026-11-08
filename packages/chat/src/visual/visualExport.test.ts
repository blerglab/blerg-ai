import { afterEach, describe, expect, it, vi } from 'vitest'
import { exportedAtLowerResolution, exportSvg, sourceExtension } from './visualExport'
import { MAX_CHART_AREA } from './vegaBudget'
import type { RenderedVisual } from './render'

const drawn = (svg: string, width: number, height: number): RenderedVisual => ({svg, width, height, dispose: vi.fn()})
afterEach(() => vi.unstubAllGlobals())

describe('visualExport', () => {
  it('names the source file by kind', () => {
    expect(sourceExtension('mermaid')).toBe('.mmd')
    expect(sourceExtension('vega-lite')).toBe('.vl.json')
  })

  it('re-sanitizes the already-drawn SVG into a downloadable blob', () => {
    const visual = drawn('<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect/></svg>', 10, 10)
    const blob = exportSvg(visual)
    expect(blob.type).toBe('image/svg+xml')
  })

  it('refuses to export SVG a diagram\'s own policy would already refuse', () => {
    // A script tag could never reach here from a real renderer (sanitizeSvg already ran once),
    // but the export path must re-check rather than trust the stored string blindly.
    const visual = drawn('<svg xmlns="http://www.w3.org/2000/svg"><script>evil()</script></svg>', 10, 10)
    expect(() => exportSvg(visual)).toThrow()
  })

  it('flags when the export will be at a lower resolution than the device pixel ratio', () => {
    vi.stubGlobal('devicePixelRatio', 2)
    // Comfortably within every budget: exportScale lands at devicePixelRatio, no clamp.
    expect(exportedAtLowerResolution(drawn('<svg/>', 400, 300))).toBe(false)
    // Already past MAX_CHART_AREA at scale 1 (a large, roughly-square Mermaid diagram, §3.7):
    // exportScale must drop below devicePixelRatio.
    const side = Math.ceil(Math.sqrt(MAX_CHART_AREA)) + 200
    expect(exportedAtLowerResolution(drawn('<svg/>', side, side))).toBe(true)
  })

  // Review L4: comparing against the raw devicePixelRatio (not exportScale's own 2x cap) would
  // call every ordinary high-DPI phone's export "lower resolution", even when nothing about this
  // particular chart triggered any clamp beyond that cap.
  it('does not flag an ordinary small chart on a 3x phone, where only exportScale\'s own 2x cap applies', () => {
    vi.stubGlobal('devicePixelRatio', 3)
    expect(exportedAtLowerResolution(drawn('<svg/>', 400, 300))).toBe(false)
  })
})
