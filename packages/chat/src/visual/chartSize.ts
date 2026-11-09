// BLG-467: the pixel box a chart or diagram draws at, shared by our own renderer (blerg/render.ts
// via render.ts) and Vega (vega.ts), so both size the same way.
export interface AvailableSize { width: number; height: number }

/** What the space around a visual is for: a chat message keeps an author's explicit width,
 *  shrinking it only when the message is narrower (visual.css's own 64rem cap handles Mermaid
 *  the same way, in CSS, since it draws through an <img> rather than a sized canvas/SVG). A tab
 *  or full screen instead fills the space it's given — explicit width or not — the one thing a
 *  single chart or diagram has to itself, so leaving most of it empty (the owner's own complaint)
 *  is always wrong there. */
export interface VisualFit { available: AvailableSize; fill: boolean; stretch?: boolean; dashboardGuides?:boolean }

/** The width/height a chart draws at. `specWidth`/`specHeight` are the author's own numbers (or
 *  undefined for a composition that lays itself out from its own panels, which never reaches
 *  this function with `fill: true` — compositions keep their authored panel sizes everywhere,
 *  vegaPolicy.ts's own comment).
 *
 *  Not filling: the explicit width is kept, only ever shrunk to fit a narrower space, scaling
 *  height to match so a resize never distorts the chart; with no explicit width, the available
 *  width drives it directly.
 *
 *  Filling (a tab or full screen): the available width drives it regardless of any explicit
 *  width — scaled up, not just down — bounded by the available height so a single chart is never
 *  taller than the visible area and never needs to scroll, still keeping its own aspect ratio. */
export function chartSize(specWidth: number | undefined, specHeight: number | undefined, available: AvailableSize, fill: boolean): AvailableSize {
  const aspectWidth = specWidth && specWidth > 0 ? specWidth : 640
  const aspectHeight = specHeight && specHeight > 0 ? specHeight : 360
  const availableWidth = Math.max(1, available.width || aspectWidth)
  if (!fill) {
    if (!(specWidth && specWidth > 0)) return {width: availableWidth, height: Math.max(1, aspectHeight)}
    const width = Math.max(1, Math.min(specWidth, availableWidth))
    return {width, height: Math.max(1, Math.round(aspectHeight * width / specWidth))}
  }
  let width = availableWidth
  let height = Math.max(1, Math.round(aspectHeight * width / aspectWidth))
  if (available.height > 0 && height > available.height) {
    height = Math.max(1, available.height)
    width = Math.max(1, Math.round(aspectWidth * height / aspectHeight))
  }
  return {width, height}
}
