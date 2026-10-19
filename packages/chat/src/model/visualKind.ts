/** Detection stays small: loading an ordinary Markdown message never imports a renderer. */
export type VisualKind = 'mermaid' | 'vega-lite'

export function visualFence(info?: string): VisualKind | null {
  const lang = info?.trim().split(/\s+/)[0].replace(/^language-/, '').toLowerCase()
  return lang === 'mermaid' || lang === 'vega-lite' ? lang : null
}

export function visualFile(name: string, view: string, source: string): VisualKind | null {
  if (view === 'text' && /\.(mmd|mermaid)$/i.test(name)) return 'mermaid'
  if (view !== 'json') return null
  if (/\.(vl|vega-lite)\.json$/i.test(name)) return 'vega-lite'
  try {
    const spec: unknown = JSON.parse(source)
    if (typeof spec !== 'object' || spec === null || !('$schema' in spec)) return null
    return typeof spec.$schema === 'string' && /^https:\/\/vega\.github\.io\/schema\/vega-lite\/v[0-9]+(?:\.[0-9]+)*\.json$/.test(spec.$schema) ? 'vega-lite' : null
  } catch {
    return null
  }
}
