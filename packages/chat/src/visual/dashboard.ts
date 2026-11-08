import {validDataset, type DatasetReference} from './datasets'
import {parseVegaLite} from './vegaPolicy'
export interface DashboardCard {
  id: string; kind: 'stat' | 'gauge' | 'text' | 'chart'; title: string; span: number
  data: {kind: 'inline'; rows: Record<string, string | number | boolean | null>[]} | DatasetReference
  view?: {value: string; selection: 'single' | 'latest'; unit?: string; delta?: string; sparkline?: string; min?: number; max?: number; thresholds?: number[]}
  spec?: Record<string, unknown>
}
export interface Dashboard {format: 'blerg-dashboard'; version: 1; title: string; cards: DashboardCard[]}
export class DashboardRefused extends Error {}
function fail(cause: string): never { throw new DashboardRefused(`Dashboard refused: ${cause}`) }
const obj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v)
const str = (v: unknown): v is string => typeof v === 'string' && v.length > 0 && v.length <= 500
const finite = (v: unknown): v is number => typeof v === 'number' && Number.isFinite(v)
function keys(v: Record<string, unknown>, allowed: string[]) {
  if (Object.keys(v).some(k => !allowed.includes(k))) fail('unknown setting')
}
/** Version 1 accepts inline rows and allow-listed scalar datasets only. */
export function parseDashboard(source: string): Dashboard {
  if (new TextEncoder().encode(source).length > 1_048_576) fail('document exceeds 1 MiB')
  let doc: unknown
  try { doc = JSON.parse(source) } catch { fail('invalid JSON') }
  if (!obj(doc)) fail('expected a document')
  keys(doc, ['format', 'version', 'title', 'cards'])
  if (doc.format !== 'blerg-dashboard' || doc.version !== 1) fail('unsupported format or version')
  if (!str(doc.title) || !Array.isArray(doc.cards) || doc.cards.length > 32) fail('expected a title and at most 32 cards')
  const ids = new Set<string>()
  let rows = 0
  for (const card of doc.cards) {
    if (!obj(card)) fail('expected a card')
    keys(card, ['id', 'kind', 'title', 'span', 'data', 'view', 'spec'])
    if (!str(card.id) || ids.has(card.id)) fail('missing or duplicate card ID')
    ids.add(card.id)
    if (!str(card.title) || !Number.isInteger(card.span) || Number(card.span) < 1 || Number(card.span) > 4) fail('invalid title or span')
    if (!['stat', 'gauge', 'text', 'chart'].includes(String(card.kind))) fail('unsupported card kind')
    if (!obj(card.data)) fail('missing data')
    const live = card.data.kind === 'dataset'
    if (live) {
      keys(card.data, ['kind', 'name', 'parameters'])
      if ((!['stat', 'gauge'].includes(String(card.kind)) && card.data.name !== 'blerg.mcp') || !validDataset(card.data)) fail('invalid scalar dataset')
    } else {
      keys(card.data, ['kind', 'rows'])
      if (card.data.kind !== 'inline' || !Array.isArray(card.data.rows) || card.data.rows.length > 10_000) fail('expected at most 10,000 inline rows')
    }
    const cardRows = live ? [] : card.data.rows as Record<string, unknown>[]
    rows += cardRows.length
    if (rows > 50_000) fail('dashboard exceeds 50,000 rows')
    for (const row of cardRows) {
      if (!obj(row) || Object.keys(row).length > 64 || Object.entries(row).some(([k, v]) => ['__proto__', 'constructor', 'prototype'].includes(k) || !(v === null || typeof v === 'boolean' || finite(v) || (typeof v === 'string' && v.length <= 4096)))) fail('rows must contain bounded scalar values')
    }
    if (card.kind === 'chart') {
      if (card.view !== undefined || !obj(card.spec) || 'data' in card.spec) fail('chart needs a spec without its own data')
      // Composed specs must not smuggle competing data sources past the adapter.
      const check = (v: unknown, depth = 0) => {
        if (depth > 32) fail('spec is nested too deeply')
        if (Array.isArray(v)) v.forEach(x => check(x, depth + 1))
        else if (obj(v)) for (const [k, x] of Object.entries(v)) { if (k === 'data' || k === 'datasets') fail('chart data belongs in inline rows'); check(x, depth + 1) }
      }
      check(card.spec)
      parseVegaLite(JSON.stringify({...card.spec, data: {values: cardRows}}), true)
    } else {
      if (card.spec !== undefined || !obj(card.view)) fail('card needs a view')
      const v = card.view
      if (live && card.data.name !== 'blerg.mcp' && v.sparkline !== undefined) fail('unsupported-live sparkline')
      if (live && card.data.name === 'blerg.cards.merged' && v.selection !== 'latest') fail('merge dataset needs latest selection')
      keys(v, ['value', 'selection', 'unit', 'delta', 'sparkline', 'min', 'max', 'thresholds'])
      if (!str(v.value) || !['single', 'latest'].includes(String(v.selection))) fail('expected a value field and selection')
      if (v.selection === 'single' && cardRows.length > 1) fail('single selection needs at most one row')
      for (const key of ['unit', 'delta', 'sparkline']) if (v[key] !== undefined && !str(v[key])) fail('invalid view field')
      if (card.kind === 'gauge' && (!finite(v.min) || !finite(v.max) || v.min >= v.max)) fail('gauge needs min < max')
      if (v.thresholds !== undefined && (!Array.isArray(v.thresholds) || v.thresholds.length > 12 || v.thresholds.some(x => !finite(x) || !finite(v.min) || !finite(v.max) || x < v.min || x > v.max))) fail('invalid thresholds')
      for (const row of cardRows) {
        const value = row[v.value]
        if (value !== undefined && value !== null && (card.kind === 'text' ? typeof value !== 'string' : !finite(value))) fail('invalid card value')
        if (typeof v.sparkline === 'string' && row[v.sparkline] != null && !finite(row[v.sparkline])) fail('invalid sparkline value')
      }
    }
  }
  return doc as unknown as Dashboard
}
