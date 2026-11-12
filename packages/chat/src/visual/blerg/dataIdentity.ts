// Datum identity is independent of row order and mutable values. Prefer an explicit id/key;
// otherwise the position/category plus series identifies a datum. Ambiguous identities are
// refused by the live adapter rather than silently rebinding a mark to another row.
export function datumKey(row: Record<string, unknown>, xField?: string, seriesField?: string): string {
  const declared = row.id ?? row.key
  return encodeURIComponent(JSON.stringify(declared !== undefined && declared !== null ? ['id', declared] : ['position', xField ? row[xField] : null, seriesField ? row[seriesField] : null])).replace(/[!'()*]/g, char => '%' + char.charCodeAt(0).toString(16))
}
export function sameDataStructure(left: Record<string, unknown>, right: Record<string, unknown>): boolean {
  const signature = (value: unknown, data = false, datasets = false): unknown => {
    if (Array.isArray(value)) return value.map(child => signature(child))
    if (!value || typeof value !== 'object') return value
    return Object.fromEntries(Object.entries(value).filter(([key]) => !(data && key === 'values')).sort(([a],[b]) => a.localeCompare(b)).map(([key, child]) => [key, datasets && Array.isArray(child) ? 'rows' : signature(child, key === 'data', key === 'datasets')]))
  }
  return JSON.stringify(signature(left)) === JSON.stringify(signature(right))
}
