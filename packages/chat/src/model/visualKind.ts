/** Detection stays small: loading an ordinary Markdown message never imports a renderer. */
export type VisualKind = 'mermaid' | 'vega-lite' | 'dashboard'

export function visualFence(info?: string): VisualKind | null {
  const lang = info?.trim().split(/\s+/)[0].replace(/^language-/, '').toLowerCase()
  return lang === 'mermaid' || lang === 'vega-lite' ? lang : null
}

export function visualFile(name: string, view: string, source: string): VisualKind | null {
  if (view === 'text' && /\.(mmd|mermaid)$/i.test(name)) return 'mermaid'
  if (view !== 'json') return null
  if (/\.dash\.json$/i.test(name)) return 'dashboard'
  if (/\.(vl|vega-lite)\.json$/i.test(name)) return 'vega-lite'
  try {
    const spec: unknown = JSON.parse(source)
    if (typeof spec !== 'object' || spec === null || !('$schema' in spec)) return null
    return typeof spec.$schema === 'string' && /^https:\/\/vega\.github\.io\/schema\/vega-lite\/v[0-9]+(?:\.[0-9]+)*\.json$/.test(spec.$schema) ? 'vega-lite' : null
  } catch {
    return null
  }
}

/** A stable id for an inline chart/diagram's pop-out tab (BLG-451): popping out the *same*
 *  unchanged block twice resolves to one tab (useSessionTabs looks tabs up by this id); a block
 *  whose source has since changed pops out as a distinct tab. Not cryptographic -- only needs to
 *  dedupe tabs and key a snapshot-store entry, not resist a deliberate collision. */
export function visualSnapshotId(kind: VisualKind, source: string): string {
  let hash = 2166136261 // FNV-1a, 32-bit
  const text = `${kind}:${source}`
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(36)
}
