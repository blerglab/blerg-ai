// Static callers keep the string drawing contract. Mounted charts capture a bounded scene
// through the same geometry helpers, then commit persistent DOM nodes without XML parsing.
export function escapeXml(value: string): string {
  return value.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')
}
type Attributes = Record<string, string | number | undefined | false>
interface SceneNode {tag: string; attributes: Attributes; children: SceneNode[]; text?: string}
let scene: Map<string, SceneNode> | undefined
let fragmentId = 0
let sceneBytes = 0
let plotBounds: {x: number; y: number; width: number; height: number} | undefined
export function currentPlotBounds() { return plotBounds }
export function recordPlotBounds(bounds: {x: number; y: number; width: number; height: number}): void {
  if (scene) plotBounds = bounds
}
const fragmentPattern = /\uE000\d+\uE001/g

function fragments(value: string): SceneNode[] {
  if (!scene) throw new Error('No scene preparation in progress')
  if (value.replace(fragmentPattern, '')) throw new Error('Unstructured native SVG fragment')
  return (value.match(fragmentPattern) ?? []).map(key => {
    const node = scene!.get(key)
    if (!node) throw new Error('Unknown native SVG fragment')
    return node
  })
}
function buildNode(node: SceneNode): SVGElement {
  const element = document.createElementNS('http://www.w3.org/2000/svg', node.tag)
  for (const [key, value] of Object.entries(node.attributes)) if (value !== undefined && value !== false) element.setAttribute(key, String(value))
  if (node.text !== undefined) element.textContent = node.text
  for (const child of node.children) element.append(buildNode(child))
  return element
}
export function prepareSvgScene(draw: () => string, titleOffset = 0): SVGSVGElement {
  const previous = scene
  const previousPlot = plotBounds
  plotBounds = undefined
  const previousBytes = sceneBytes
  sceneBytes = 0
  scene = new Map()
  try {
    const roots = fragments(draw())
    if (roots.length !== 1 || roots[0].tag !== 'svg') throw new Error('Invalid native SVG scene')
    const root = buildNode(roots[0]) as SVGSVGElement
    const measured = currentPlotBounds()
    if (measured) root.dataset.plotBounds = JSON.stringify([measured.x, measured.y + titleOffset, measured.width, measured.height])
    return root
  } finally { scene = previous; sceneBytes = previousBytes; plotBounds = previousPlot }
}
/** Measure a guide pass without retaining its scene nodes or plot bounds. */
export function probeSvgLayout<T>(draw: () => T): T {
  const previous = scene, previousBytes = sceneBytes, previousPlot = plotBounds
  scene = new Map(); sceneBytes = 0; plotBounds = undefined
  try { return draw() }
  finally { scene = previous; sceneBytes = previousBytes; plotBounds = previousPlot }
}
export function attrs(values: Attributes): string {
  return Object.entries(values).filter(([, value]) => value !== undefined && value !== false)
    .map(([key, value]) => ` ${key}="${typeof value === 'string' ? escapeXml(value) : value}"`).join('')
}
/** Tag/attribute names are renderer-owned. Text is always a text node, never markup. */
export function element(tag: string, attributes: Attributes = {}, children: readonly string[] = [], value?: string): string {
  if (scene) {
    sceneBytes += 8 + tag.length * 2 + (value?.length ?? 0) * 6 + Object.entries(attributes).reduce((size, [key, attr]) => size + key.length + String(attr ?? '').length * 6 + 4, 0)
    if (sceneBytes > 2_000_000) throw new Error('Native scene is too large')
    if (scene.size >= 20_000) throw new Error('Native scene is too large')
    const key = `\uE000${++fragmentId}\uE001`
    scene.set(key, {tag, attributes, children: fragments(children.join('')), text: value})
    return key
  }
  return `<${tag}${attrs(attributes)}>${value === undefined ? children.join('') : escapeXml(value)}</${tag}>`
}
export function rect(x: number, y: number, width: number, height: number, attributes: Attributes = {}): string {
  return element('rect', {x, y, width, height, ...attributes})
}
export function circle(cx: number, cy: number, r: number, attributes: Attributes = {}): string {
  return element('circle', {cx, cy, r, ...attributes})
}
export function line(x1: number, y1: number, x2: number, y2: number, attributes: Attributes = {}): string {
  return element('line', {x1, y1, x2, y2, ...attributes})
}
export function path(attributes: Attributes): string { return element('path', attributes) }
export function text(x: number, y: number, value: string, attributes: Attributes = {}): string {
  return element('text', {x, y, ...attributes}, [], value)
}
export function group(children: readonly string[], attributes: Attributes = {}): string {
  return element('g', attributes, children)
}
let chartId = 0
export function svgDocument(width: number, height: number, body: string, background = 'transparent', strokeScale = 1): string {
  const id = `blerg-chart-${++chartId}`
  const style = element('style', {'data-scene-key': 'stroke-scale'}, [], `#${id}:focus{outline:none}#${id}:focus-visible{outline:1px solid currentColor;outline-offset:2px}#${id}[data-chart-emphasis] [data-datum-key]:not([data-chart-active]){filter:opacity(.6)}#${id} [data-chart-active]{filter:drop-shadow(0 0 2px currentColor)}${strokeScale === 1 ? '' : `#${id} line,#${id} path,#${id} circle{stroke-width:${strokeScale}px}`}`)
  return element('svg', {xmlns: 'http://www.w3.org/2000/svg', id, width, height, viewBox: `0 0 ${width} ${height}`}, [style, rect(0, 0, width, height, {fill: background, 'data-scene-key': 'background'}), body])
}
