// BLG-452 design doc §3.10: utilities for the real-browser differential gate. Test-only — these
// read a live Vega `View`'s scenegraph/scale/data, which only the test-only seam
// (vegaRuntime.ts's compileVegaViewForDifferentialTesting) exposes. Never imported by production
// code.
//
// Scenegraph shape confirmed empirically against vega 6.4.0 (not guessed from source alone): a
// "mark definition" node carries `marktype`/`role` but no x/y of its own; its `items` are either
// further mark-definition nodes (if marktype === 'group') or leaf item instances, which carry
// real x/y/datum and no marktype/role. Axis tick/label mark-definition nodes have role
// 'axis-tick'/'axis-label'; their items each carry `datum.value`/`datum.label`. Mark-definition
// nodes for the actual drawn data carry role 'mark'. See vega-parser's AxisRole/AxisTickRole/
// AxisLabelRole/MarkRole constants and vega-encode's AxisTicks transform (produces exactly
// {value, label} per generated tick) for where these names come from.
import type { View } from 'vega'

interface SceneItem {
  items?: SceneItem[]
  marktype?: string
  role?: string
  x?: number
  y?: number
  width?: number
  height?: number
  datum?: {value?: unknown; label?: string}
}

export interface AxisTick { value: unknown; label?: string; x: number; y: number }
export interface Point { x: number; y: number }

function sceneRoot(view: View): SceneItem {
  return (view.scenegraph() as unknown as {root: SceneItem}).root
}

/** Every axis-tick/axis-label item's value, label, and absolute position (the view's own pixel
 *  frame, before translating into the plot-relative frame — see relativeTo below). */
export function axisTicks(view: View): AxisTick[] {
  const ticks: AxisTick[] = []
  function visit(node: SceneItem, x: number, y: number) {
    const nx = x + (node.x ?? 0)
    const ny = y + (node.y ?? 0)
    if (node.role === 'axis-tick' || node.role === 'axis-label') {
      for (const item of node.items ?? []) {
        if (item.datum && 'value' in item.datum) ticks.push({value: item.datum.value, label: item.datum.label, x: nx + (item.x ?? 0), y: ny + (item.y ?? 0)})
      }
    }
    for (const item of node.items ?? []) visit(item, nx, ny)
  }
  visit(sceneRoot(view), 0, 0)
  return ticks
}

/** The top-left corner of the bounding box of every item whose mark-definition role is 'mark'
 *  (the actual drawn data, excluding axis/legend/title groups) — the plot rectangle's own origin,
 *  design doc §3.10's "the plot-origin frame." Infinity/-Infinity if the view draws no marks.
 *  Only reads each item's own x/y anchor, not x2/y2 — fine for single-anchor marks (bar, point,
 *  text), but a mark whose bounding box extends past that anchor (rule's x2/y2 endpoint, an
 *  area's full path) needs its own origin derived from its own full point set instead; see
 *  BlergRule.differential.browser.test.tsx for why and how. */
export function plotOrigin(view: View): Point {
  let minX = Infinity, minY = Infinity
  function visit(node: SceneItem, x: number, y: number) {
    const nx = x + (node.x ?? 0)
    const ny = y + (node.y ?? 0)
    if (node.role === 'mark') {
      for (const item of node.items ?? []) {
        minX = Math.min(minX, nx + (item.x ?? 0))
        minY = Math.min(minY, ny + (item.y ?? 0))
      }
    }
    for (const item of node.items ?? []) visit(item, nx, ny)
  }
  visit(sceneRoot(view), 0, 0)
  return {x: minX, y: minY}
}

/** Translates an absolute position into the plot-relative frame (design doc §3.10): subtracting
 *  the plot's own origin cancels out axis/legend/title padding differences between the two
 *  renderers' text measurement, leaving only geometry that's actually comparable. */
export function relativeTo(origin: Point, point: Point): Point {
  return {x: point.x - origin.x, y: point.y - origin.y}
}

/** §3.10's stated tolerance for plot-relative positions and non-continuous tick placement (the
 *  cases not backed by the shared d3-array `ticks()` call, §3.4). Continuous-scale tick *values*
 *  are checked for exact equality elsewhere, not through this tolerance. */
export const POSITION_TOLERANCE_PX = 0.5

export function withinTolerance(a: number, b: number, tolerance: number = POSITION_TOLERANCE_PX): boolean {
  return Math.abs(a - b) <= tolerance
}

/** Review finding: every differential gate checked geometry/color but none compared the actual
 *  *tick values* an axis draws — the bug that let this renderer's tick-count/format drift from
 *  Vega's go unnoticed (11 y-ticks drawn here vs. Vega's 5 on a line chart, 14 vs. 7 on a bar
 *  chart) until a manual screenshot review caught it. Vega-Lite compiles a quantitative x axis
 *  before a quantitative y axis (confirmed empirically, not assumed, across every mark this
 *  renderer draws), so the first axis-label group found in scenegraph order is the x axis's own,
 *  the second is the y axis's — this reads Vega's OWN drawn tick values directly from its
 *  scenegraph, not a value this file recomputes, so a real mismatch can't be masked by both
 *  sides sharing the same (possibly wrong) assumption. */
export function axisValues(view: View, axis: 'x' | 'y'): unknown[] {
  const root = sceneRoot(view)
  const groups: unknown[][] = []
  function visit(node: SceneItem) {
    if (node.role === 'axis-label' && node.marktype === 'text') groups.push((node.items ?? []).map(item => item.datum?.value))
    for (const item of node.items ?? []) visit(item)
  }
  visit(root)
  return groups[axis === 'x' ? 0 : 1] ?? []
}

/** Vega's own drawn tick *label text* (not the raw numeric value — Vega applies its own format,
 *  e.g. "25,000" not "25000"), for comparing against this renderer's own drawn label text
 *  directly, format included. */
export function axisLabels(view: View, axis: 'x' | 'y'): string[] {
  const root = sceneRoot(view)
  const groups: string[][] = []
  function visit(node: SceneItem) {
    if (node.role === 'axis-label' && node.marktype === 'text') groups.push((node.items ?? []).map(item => item.datum?.label ?? String(item.datum?.value ?? '')))
    for (const item of node.items ?? []) visit(item)
  }
  visit(root)
  return groups[axis === 'x' ? 0 : 1] ?? []
}

/** Every item drawn by a "mark" (role === 'mark', the actual data — never axis/legend/title
 *  chrome) whose marktype is in the given set, with its absolute bounding box in the view's own
 *  pixel frame. Generic across mark types (bar's rect, point's symbol, line/area/arc's line/area/
 *  arc, text's text, rule's rule) — the generated corpus's own differential gate uses this to
 *  compare drawn item *count* uniformly, without a separate hand-written extractor per mark. */
export interface MarkItem { x: number; y: number; width: number; height: number }
export function markItems(view: View, marktypes: ReadonlySet<string>): MarkItem[] {
  const out: MarkItem[] = []
  function visit(node: SceneItem, x: number, y: number) {
    const nx = x + (node.x ?? 0), ny = y + (node.y ?? 0)
    if (node.role === 'mark' && node.marktype && marktypes.has(node.marktype)) {
      for (const item of node.items ?? []) out.push({x: nx + (item.x ?? 0), y: ny + (item.y ?? 0), width: item.width ?? 0, height: item.height ?? 0})
    }
    for (const item of node.items ?? []) visit(item, nx, ny)
  }
  visit(sceneRoot(view), 0, 0)
  return out
}

/** Counts "mark" role *groups* (not their items) whose marktype is in the given set — a
 *  line/area mark's own scenegraph group is one per series, each holding one item per data
 *  point, so series count (what this renderer draws one <path> per) needs this, not markItems'
 *  per-item count. */
export function markGroupCount(view: View, marktypes: ReadonlySet<string>): number {
  let count = 0
  function visit(node: SceneItem) {
    if (node.role === 'mark' && node.marktype && marktypes.has(node.marktype)) count++
    for (const item of node.items ?? []) visit(item)
  }
  visit(sceneRoot(view))
  return count
}

/** Custom-sort follow-up: Vega's own drawn categorical legend entries, in scenegraph (= drawn,
 *  top-to-bottom) order — confirmed empirically to carry the color-domain's own sort order, the
 *  same way axisValues already proves an x/y band axis's. */
export function legendLabels(view: View): string[] {
  const root = sceneRoot(view)
  const labels: string[] = []
  function visit(node: SceneItem) {
    if (node.role === 'legend-label' && node.marktype === 'text') {
      for (const item of node.items ?? []) if (item.datum?.value !== undefined) labels.push(String(item.datum.value))
    }
    for (const item of node.items ?? []) visit(item)
  }
  visit(root)
  return labels
}
