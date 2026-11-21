import { ChartRefused } from './vegaPolicy'

export const HEAVY_CHART = 'This chart is too heavy to draw'
export const CHART_DEADLINE_MS = 5000
export const MAX_CHART_SIDE = 4096
export const MAX_CHART_AREA = 4096 * 2048
export const CHART_LIMITS = {
  rows: 100_000,
  rowSlots: 250_000,
  operators: 10_000,
  sceneItems: 20_000,
  panels: 64,
  svgBytes: 2_000_000,
} as const
export type ChartLimits = { [K in keyof typeof CHART_LIMITS]: number }
export interface TooltipRegion { x1: number; y1: number; x2: number; y2: number; text: string; key?: string; bar?: boolean }
export interface ChartSize { width: number; height: number; maxHeight?: number }
export interface ChartDrawing { svg: string; width: number; height: number; regions: TooltipRegion[]; layout?: {plot: [number, number, number, number]; labels: number; legendLabels: number} }
export function heavyChart(): never { throw new ChartRefused(HEAVY_CHART) }

/** The scale an export's own off-screen canvas uses (visualExport.ts) — a sibling to the display
 *  path's own scale in vega.ts, not a refactor of it: that one keeps its `Math.max(1, …)` floor
 *  because Vega-Lite's chart is already area-clamped before it applies. Export also serves
 *  Mermaid, whose width/height are only clamped per side (mermaid.ts), so a large, roughly-square
 *  diagram can already exceed MAX_CHART_AREA at scale 1 — exportScale has no floor, so it can
 *  legitimately return less than 1; the caller states that in the UI rather than silently
 *  shrinking. Lives here, not in vega.ts, so visualExport.ts (used by every chart/diagram tab,
 *  including Mermaid-only ones) never statically pulls in Vega-Lite's own bundle — render.ts's
 *  `await import('./vega')` stays the only, lazy entry point into it. */
export function exportScale(width: number, height: number): number {
  return Math.min(devicePixelRatio || 1, 2, MAX_CHART_SIDE / width, MAX_CHART_SIDE / height,
    Math.sqrt(MAX_CHART_AREA / (width * height)))
}
export function tooltipText(value: unknown): string {
  if (value === null || value === undefined) return ''
  if (typeof value === 'object') {
    return Object.entries(value).map(([key, item]) => `${key}: ${typeof item === 'object' ? JSON.stringify(item) : String(item)}`).join('\n').slice(0, 4000)
  }
  return String(value).slice(0, 4000)
}
