import { readVisualTheme, type VisualTheme } from './theme'
import { parseVegaLite, validateMermaid, type VisualKind } from './policy'

export async function renderVisual(kind: VisualKind, source: string, container: HTMLElement, theme: VisualTheme = readVisualTheme(container)): Promise<() => void> {
  if (kind === 'mermaid') {
    validateMermaid(source)
    return (await import('./mermaid')).renderMermaid(source, container, theme)
  }
  const spec = parseVegaLite(source)
  return (await import('./vega')).renderVegaLite(spec, container, theme)
}
