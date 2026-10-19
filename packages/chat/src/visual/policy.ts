// Agent output is untrusted. Validate before importing a renderer or giving it DOM access.
export type VisualKind = 'mermaid' | 'vega-lite'
export const MAX_VISUAL_SOURCE = 131_072
const SVG_NS = 'http://www.w3.org/2000/svg'

export function validateMermaid(source: string): void {
  if (!source.trim() || source.length > 16_384 || (source.match(/-->|==>|-\.->|--/g)?.length ?? 0) > 500) {
    throw new Error('Diagram is empty or too large')
  }
  if (/^\s*---|%%\s*\{|<\/?[a-z!]|\b(?:click|link|links|style|classDef|linkStyle|image|img|icon)\b|(?:https?|data|file|javascript|vbscript):|url\s*\(|!\s*\[|\]\s*\(|\$\$|@import/i.test(source)) {
    throw new Error('Diagram contains unsupported active content or configuration')
  }
}

const MARKS = new Set(['bar', 'line', 'point', 'circle', 'square', 'rect', 'area', 'tick', 'rule', 'text', 'trail', 'arc'])
const FORBIDDEN = new Set(['url', 'href', 'expr', 'signal', 'calculate', 'transform', 'bind', 'params', 'selection', 'config', 'layer', 'hconcat', 'vconcat', 'concat', 'facet', 'repeat', 'spec', '$ref', '__proto__', 'prototype', 'constructor'])

export function parseVegaLite(source: string): Record<string, unknown> {
  if (!source.trim() || source.length > MAX_VISUAL_SOURCE) throw new Error('Chart is empty or too large')
  const root: unknown = JSON.parse(source)
  if (!root || typeof root !== 'object' || Array.isArray(root)) throw new Error('Chart must be an object')
  let nodes = 0
  let marks = 0
  function walk(value: unknown, depth: number, inline = false, context = ''): void {
    if (++nodes > 30_000 || depth > 40) throw new Error('Chart is too complex')
    if (typeof value === 'number' && !Number.isFinite(value)) throw new Error('Chart numbers must be finite')
    if (Array.isArray(value)) {
      if (value.length > 10_000) throw new Error('Chart has too many values')
      for (const child of value) walk(child, depth + 1, inline, context)
    } else if (value && typeof value === 'object') {
      for (const [key, child] of Object.entries(value)) {
        // Data values are inert, but reject dangerous object keys throughout the tree.
        if (['__proto__', 'prototype', 'constructor'].includes(key)) throw new Error('Unsupported chart key')
        if (!inline) {
          if (FORBIDDEN.has(key.toLowerCase()) || /(?:expr|signal)$/i.test(key) || (['filter', 'test'].includes(key) && typeof child === 'string')) throw new Error('Chart resources and custom expressions are disabled')
          if (key === '$schema' && (typeof child !== 'string' || !/^https:\/\/vega\.github\.io\/schema\/vega-lite\/v\d+(?:\.\d+)*\.json$/.test(child))) throw new Error('Unsupported chart schema')
          if (key === 'data') {
            if (!child || typeof child !== 'object' || Array.isArray(child) || !Object.hasOwn(child, 'values') || !Array.isArray((child as {values?: unknown}).values) || Object.keys(child).some(k => k !== 'values')) throw new Error('Charts require inline data.values')
          }
          if (key === 'mark') {
            if (++marks > 32) throw new Error('Chart has too many panels')
            const mark = typeof child === 'string' ? child : child && typeof child === 'object' && 'type' in child ? child.type : null
            if (typeof mark !== 'string' || !MARKS.has(mark)) throw new Error('Unsupported chart mark')
          }
          if (['repeat', 'layer', 'concat', 'hconcat', 'vconcat'].includes(key) && Array.isArray(child) && child.length > 16) throw new Error('Chart has too many panels')
          if (key === 'bin' && child !== true && child !== false && child !== 'binned') throw new Error('Custom chart bin generators are disabled')
          if (key === 'bins' && (!Array.isArray(child) || child.length > 1000)) throw new Error('Chart bins require bounded explicit values')
          if (key === 'tickCount' && (typeof child !== 'number' || child < 1 || child > 1000)) throw new Error('Chart ticks require a bounded numeric count')
          if (['tickCount', 'maxbins', 'steps', 'maxsteps'].includes(key) && typeof child === 'number' && (child < 0 || child > 1000)) throw new Error('Chart rendering count is too large')
          if ((key === 'width' || key === 'height') && (typeof child !== 'number' || !Number.isFinite(child) || child < 1 || child > 1024)) throw new Error('Charts require bounded numeric dimensions')
        }
        walk(child, depth + 1, inline || (context === 'data' && key === 'values') || (depth === 0 && key === 'datasets'), key)
      }
    }
  }
  walk(root, 0)
  if (!('mark' in root)) throw new Error('Chart has no marks')
  // Fixed outer dimensions prevent categorical rangeStep/autosize from allocating an
  // unbounded canvas. Single plots only; compound/generative specs fall back to source.
  return {...root, width: 'width' in root ? root.width : 640, height: 'height' in root ? root.height : 360,
    autosize: {type: 'fit', contains: 'padding'}, padding: 16, background: 'transparent'}
}

const SVG_TAGS = new Set(['svg', 'g', 'defs', 'marker', 'path', 'rect', 'circle', 'ellipse', 'line', 'polyline', 'polygon', 'text', 'tspan', 'title', 'desc', 'style', 'clipPath', 'linearGradient', 'radialGradient', 'stop', 'pattern', 'filter', 'feDropShadow', 'symbol'])
function safeStyle(value: string): boolean {
  // Only local fragment paint/clip references are useful in inert diagram images.
  const withoutLocal = value.replace(/url\(\s*['"]?#[\w:.-]+['"]?\s*\)/gi, '')
    .replace(/@(?:-webkit-)?keyframes\s+[\w-]+/gi, '')
  return !/url\s*\(|image-set\s*\(|@|\\|(?:https?|data|file|javascript|vbscript):|expression\s*\(|</i.test(withoutLocal)
}

export function sanitizeSvg(source: string): string {
  if (source.length > 2_000_000 || /<!DOCTYPE|<!ENTITY/i.test(source)) throw new Error('Unsupported SVG')
  const doc = new DOMParser().parseFromString(source, 'image/svg+xml')
  if (doc.querySelector('parsererror') || doc.documentElement.localName !== 'svg') throw new Error('Invalid SVG')
  for (const el of [doc.documentElement, ...doc.documentElement.querySelectorAll('*')]) {
    if (el.namespaceURI !== SVG_NS || !SVG_TAGS.has(el.localName)) throw new Error('Unsupported SVG element')
    for (const attr of el.attributes) {
      if (/^on/i.test(attr.name) || attr.localName === 'href' || (attr.namespaceURI && attr.namespaceURI !== 'http://www.w3.org/2000/xmlns/') || (!attr.name.startsWith('xmlns') && !safeStyle(attr.value))) throw new Error('Unsupported SVG attribute')
    }
    if (el.localName === 'style' && !safeStyle(el.textContent ?? '')) throw new Error('Unsupported SVG styles')
  }
  return new XMLSerializer().serializeToString(doc.documentElement)
}
