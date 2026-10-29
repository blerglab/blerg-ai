// Agent output is untrusted. Validate before importing a renderer or giving it DOM access.
export type VisualKind = 'mermaid' | 'vega-lite'
export const MAX_VISUAL_SOURCE = 131_072
const SVG_NS = 'http://www.w3.org/2000/svg'

const MERMAID_MAX = 16_384

// What a diagram may not contain, wherever it appears: anything that fetches, links, runs or
// reconfigures the renderer. Labels are free text: a word such as "image" or "style" in a label
// is fine; the image syntax, HTML and URLs are not. Checked on the normalized source with comment
// lines blanked, so the reason can name the line.
const MERMAID_REFUSED: Array<[RegExp, string]> = [
  [/<\/?[a-z!]/i, 'HTML'],
  [/(?:^|;) *click\b/im, 'a click action'],
  [/(?:^|;) *links? +[^\n:;]*:/im, 'a link'],
  [/::icon *\(/i, 'an icon'],
  [/(?:https?|ftp|data|file|blob|javascript|vbscript):/i, 'a URL'],
  [/\/\//, 'a //host address'],
  [/url *\(/i, 'url()'],
  [/image-set *\(|expression *\(/i, 'a CSS function'],
  [/! *\[/, 'a Markdown image'],
  [/\] *\(/, 'a Markdown link'],
  [/\$\$/, 'math ($$)'],
  [/@import/i, '@import'],
]

/** Color styling a diagram may set (classDef, style, linkStyle), and the values they may take:
 *  colors, numbers, lengths and keywords; no functions, quotes, escapes, slashes or "!important". */
const MERMAID_STYLE_PROPS = new Set(['fill', 'stroke', 'color', 'stroke-width', 'stroke-dasharray', 'font-weight', 'font-style', 'opacity', 'radius'])
const MERMAID_STYLE_VALUE = /^[#a-z0-9.% -]{1,64}$/i

/** The keys an `A@{ … }` shape (or `e1@{ … }` edge) block may set, each with a literal value: a
 *  plain word or number, or a double-quoted label without escapes or braces. Kanban may also
 *  assign a task and its priority. Never img, icon or
 *  any other key that loads something; never a quoted key. */
const MERMAID_SHAPE_KEYS = new Set(['shape', 'label', 'w', 'h', 'pos', 'constraint', 'animate', 'animation', 'curve', 'assigned', 'priority'])
const MERMAID_SHAPE_ENTRY = /^\s*([a-z]+) *: *("[^"\\{}\n]*"|[\w.%-]+) *(,|\n|$)/i

/** Why a diagram is refused: "line 12: url() not allowed". */
export class DiagramRefused extends Error {}

function refuse(line: number, what: string): never {
  throw new DiagramRefused(`line ${line}: ${what} not allowed`)
}

/** The source as Mermaid reads its lines: every line ending (\r\n, \r, U+2028, U+2029, U+0085)
 *  becomes \n, and every other space-like character (no-break, ideographic, zero-width, BOM, tab)
 *  becomes an ordinary space. The validator checks this string, and only this string is drawn. */
export function normalizeMermaid(source: string): string {
  return source.replace(/\r\n?|[\u2028\u2029\u0085]/g, '\n').replace(/[^\S\n]|[\u180e\u200b-\u200d\u2060]/g, ' ')
}

/** Checks a diagram and returns the normalized source to draw (see normalizeMermaid). */
export function validateMermaid(raw: string): string {
  const source = normalizeMermaid(raw)
  if (!source.trim()) throw new DiagramRefused('the diagram is empty')
  if (source.length > MERMAID_MAX) throw new DiagramRefused(`the diagram is too large (${source.length} characters; the limit is ${MERMAID_MAX})`)
  if ((source.match(/-->|==>|-\.->|--/g)?.length ?? 0) > 500) throw new DiagramRefused('the diagram has more than 500 links')
  const lines = source.split('\n')
  const lineOf = (index: number) => source.slice(0, index).split('\n').length
  // Directives reconfigure the renderer (its security level, its CSS).
  const directive = /%% *\{/.exec(source)
  if (directive) refuse(lineOf(directive.index), 'a configuration directive (%%{…}%%)')
  // Front matter: a title only (config could change the security level or inject CSS).
  let body = 0
  if (/^\s*---/.test(source)) {
    // Closed where Mermaid closes it: the first later line that is --- at its start.
    const open = lines.findIndex(line => line.trim() !== '')
    const close = lines.findIndex((line, i) => i > open && /^-{3} *$/.test(line))
    if (close < 0) refuse(open + 1, 'front matter without its closing ---')
    for (let i = open + 1; i < close; i++) {
      const line = lines[i]
      if (!line.trim()) continue
      if (line.length > 200 || !/^title: *(?:"[^"\\]*"|'[^']*'|[^\s!&*{}[\]|>@%`#'"][^{}[\]]*)? *$/.test(line)) {
        refuse(i + 1, `front matter other than a title ("${line.trim().split(':')[0].slice(0, 40)}")`)
      }
    }
    body = close + 1
  }
  // Comments are dropped by Mermaid: blank them (same length, so indexes keep their lines).
  // Class annotations (<<interface>>) and line breaks (<br/>) are not HTML.
  const checked = lines.map((line, i) => i < body && !/^ *title:/.test(line) ? ' '.repeat(line.length)
    : /^ *%%/.test(line) ? ' '.repeat(line.length)
      : line.replace(/<<[\w -]{1,40}>>/g, m => ' '.repeat(m.length)).replace(/<br *\/?>/gi, m => ' '.repeat(m.length))).join('\n')
  for (const [pattern, what] of MERMAID_REFUSED) {
    const hit = pattern.exec(checked)
    if (hit) refuse(lineOf(hit.index), what)
  }
  checkShapes(checked, lineOf)
  // Flowcharts terminate style statements at semicolons. Other grammars (notably state)
  // consume the rest of the CSS line, including text that looks like another statement.
  // Only flowcharts may stop at an inline class assignment; never leave a CSS tail unchecked.
  // flow.jison uses CLASS SPACE idString SPACE idString: both operands can contain Unicode,
  // commas, and punctuation. Recognize the two operands here; let Mermaid parse their tokens.
  const diagram = checked.split('\n').slice(body).find(line => line.trim()) ?? ''
  const styleStatement = /^ *(?:flowchart|graph)\b/i.test(diagram)
    ? /(?:^|;) *(classDef|style|linkStyle)\b(.*?)(?=; *(?:classDef|style|linkStyle)\b|; *class +[^ ;]+ +[^ ;]+ *(?:;|$)|$)/g
    : /(?:^|;) *(classDef|style|linkStyle)\b(.*)$/g
  checked.split('\n').forEach((line, i) => {
    if (i < body) return
    for (const m of line.matchAll(styleStatement)) checkStyle(m[1], m[2], i + 1)
  })
  return source
}

function checkShapes(checked: string, lineOf: (index: number) => number) {
  for (const open of checked.matchAll(/@ *\{/g)) {
    const start = open.index + open[0].length
    const end = checked.indexOf('}', start)
    const line = lineOf(open.index)
    if (end < 0) refuse(line, 'an unclosed @{…} block')
    let rest = checked.slice(start, end).trim()
    while (rest) {
      const entry = MERMAID_SHAPE_ENTRY.exec(rest)
      if (!entry) refuse(line, `this @{…} block ("${rest.slice(0, 40)}")`)
      if (!MERMAID_SHAPE_KEYS.has(entry[1].toLowerCase())) refuse(line, `the @{…} key "${entry[1].slice(0, 40)}"`)
      rest = rest.slice(entry[0].length).trim()
    }
  }
}

function checkStyle(keyword: string, rest: string, line: number) {
  rest = rest.trim()
  const target = keyword === 'linkStyle'
    ? /^(?:default|\d+(?: *, *\d+)*)(?: +interpolate +[a-z]+)?(?: +|$)/i.exec(rest)
    : /^[\w.-]+(?: *, *[\w.-]+)*(?: +|$)/.exec(rest)
  if (!target) refuse(line, `this ${keyword}`)
  for (const prop of rest.slice(target[0].length).split(/[,;]/)) {
    if (!prop.trim()) continue
    const [name, ...value] = prop.split(':')
    const key = name.trim().toLowerCase()
    if (!MERMAID_STYLE_PROPS.has(key)) refuse(line, `the style property "${key.slice(0, 40)}"`)
    if (!MERMAID_STYLE_VALUE.test(value.join(':').trim())) refuse(line, `the ${key} value "${value.join(':').trim().slice(0, 40)}"`)
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
    if (el.namespaceURI !== SVG_NS || !SVG_TAGS.has(el.localName)) throw new Error(`Unsupported SVG element: ${el.localName}`)
    for (const attr of el.attributes) {
      const xmlSpace = attr.namespaceURI === 'http://www.w3.org/XML/1998/namespace'
        && attr.localName === 'space' && /^(preserve|default)$/.test(attr.value)
      if (/^on/i.test(attr.name) || attr.localName === 'href' || (attr.namespaceURI && attr.namespaceURI !== 'http://www.w3.org/2000/xmlns/' && !xmlSpace) || (!attr.name.startsWith('xmlns') && !safeStyle(attr.value))) throw new Error(`Unsupported SVG attribute: ${attr.name}`)
    }
    if (el.localName === 'style' && !safeStyle(el.textContent ?? '')) throw new Error('Unsupported SVG styles')
  }
  return new XMLSerializer().serializeToString(doc.documentElement)
}
