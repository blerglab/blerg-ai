import { sanitizeSvg } from './policy'
// From vegaBudget.ts, not vega.ts: a static import of vega.ts here would pull Vega-Lite's whole
// bundle into every chart/diagram tab, including a Mermaid-only one -- render.ts's lazy
// `await import('./vega')` is meant to be the only entry point into it.
import { exportScale } from './vegaBudget'
import type { RenderedVisual } from './render'
import type { VisualTheme } from './theme'

// Dashboard values have no payload-export authorization/expiry boundary yet.
// Both viewers must refuse even a stale RenderedVisual from a prior document.
export const DASHBOARD_EXPORT_BLOCKED = 'Dashboard data exports are disabled. Download source contains binding references only for MCP cards.'
export function payloadExportAllowed(kind: 'mermaid' | 'vega-lite' | 'dashboard' | null): boolean {
  return kind === 'mermaid' || kind === 'vega-lite'
}

/** The source text as a downloadable file: `.mmd` for Mermaid, `.vl.json` for Vega-Lite. */
export function sourceExtension(kind: 'mermaid' | 'vega-lite' | 'dashboard'): string {
  return kind === 'dashboard' ? '.dash.json' : kind === 'mermaid' ? '.mmd' : '.vl.json'
}

/** Re-sanitizes the already-sanitized `RenderedVisual.svg` before download — defense in depth
 *  (the failure rule), not a second opinion on the original diagram source: this never re-renders
 *  from `source`, only re-checks the bytes the renderer already drew. */
export function exportSvg(visual: RenderedVisual): Blob {
  return new Blob([sanitizeSvg(visual.svg)], {type: 'image/svg+xml'})
}

/** True when `exportPng` will draw at less than the device's own achievable pixel ratio because
 *  `exportScale` had to clamp further, to stay within the shared size/area budget — not because
 *  of `exportScale`'s own, always-present 2x cap, which every device above 2x already expects
 *  (comparing against the raw, uncapped `devicePixelRatio` would call that ordinary cap a
 *  reduction on any 3x phone). The caller states this in the UI; export never does it silently. */
export function exportedAtLowerResolution(visual: RenderedVisual): boolean {
  const achievable = Math.min(typeof devicePixelRatio === 'number' ? devicePixelRatio : 1, 2)
  return exportScale(visual.width, visual.height) < achievable
}

/** Rasterizes the already-sanitized SVG at `exportScale`, on the resolved theme's surface color
 *  (so a PNG pasted onto a page with its own background doesn't show a transparency
 *  checkerboard), from a fresh decode -- the display's own decoded image may already be disposed
 *  by the time this is called. Renderer-agnostic: operates only on RenderedVisual. */
export async function exportPng(visual: RenderedVisual, theme: VisualTheme): Promise<Blob> {
  const safe = sanitizeSvg(visual.svg)
  const image = new Image()
  image.src = 'data:image/svg+xml;charset=utf-8,' + encodeURIComponent(safe)
  await image.decode()
  const scale = exportScale(visual.width, visual.height)
  const canvas = document.createElement('canvas')
  canvas.width = Math.max(1, Math.round(visual.width * scale))
  canvas.height = Math.max(1, Math.round(visual.height * scale))
  const context = canvas.getContext('2d')
  if (!context) throw new Error('Canvas is unavailable')
  context.fillStyle = theme.surface
  context.fillRect(0, 0, canvas.width, canvas.height)
  context.scale(scale, scale)
  context.drawImage(image, 0, 0, visual.width, visual.height)
  return new Promise<Blob>((resolve, reject) => {
    try {
      canvas.toBlob(blob => { if (blob) resolve(blob); else reject(new Error('Could not create the image')) }, 'image/png')
    } catch (error) {
      reject(error instanceof Error ? error : new Error('Could not create the image'))
    }
  })
}
