import {classifyChart} from './blerg/classify'
/** Dashboard charts share a clock format and horizontal category labels.
 * Authored guide settings remain authoritative; there is no expression mapping. */
export function dashboardChart(spec: Record<string, unknown>, rows: unknown[], forceFallback=false) {
  const native={...spec,data:{values:rows}}
  // Native guides already fit the measured pane; injected Vega guide defaults would
  // force otherwise native templates onto the static fallback. Authored settings still win.
  if(!forceFallback && classifyChart(native)==='blerg')return JSON.stringify(native)
  const visit = (node: unknown): unknown => {
    if (Array.isArray(node)) return node.map(visit)
    if (!node || typeof node !== 'object') return node
    const result = Object.fromEntries(Object.entries(node).map(([k, v]) => [k, visit(v)]))
    const encoding = result.encoding as Record<string, Record<string, unknown>> | undefined
    const x = encoding?.x
    if (x && x.axis !== null) {
      const domain = (x.scale as {domain?: unknown[]})?.domain
      const values = Array.isArray(domain) ? domain : rows.flatMap(row => {
        const value = row && typeof row === 'object' ? (row as Record<string, unknown>)[String(x.field)] : null
        return value == null ? [] : [value]
      })
      const days = new Set(values.flatMap(value => {
        const date = typeof value === 'string' || typeof value === 'number' ? new Date(value) : null
        return date && Number.isFinite(date.getTime()) ? [date.toISOString().slice(0, 10)] : []
      }))
      x.axis = {labelAngle: 0, labelOverlap: true, labelPadding: 8, ...(x.type === 'temporal' ? {format: days.size > 1 ? '%b %d' : '%H:%M'} : {}), ...(x.axis as object ?? {})}
    }
    return result
  }
  return JSON.stringify({...visit(spec) as object, data: {values: rows}})
}
