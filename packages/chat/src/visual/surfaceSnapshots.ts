// Bounded, in-memory transfer snapshots. These are complete drawings, never live surfaces:
// closing a tab cannot dispose another view's model, listeners or worker. No persistence.
import {BoundedCache, sourceHash} from './boundedCache'
interface Snapshot {drawing: Element; width: number; height: number; plot?: string; labels: string; legend: string}
let snapshotId = 0
const snapshots = new BoundedCache<Snapshot>(16 << 20)
const MAX_PIXELS = 4_194_304
const providers = new Map<string, Set<() => void>>()
/** Read only at transfer/hide/unmount, never clone on each resize commit. */
export function offerSurface(source: string, capture: () => void): () => void {
  const key = sourceHash(source), group = providers.get(key) ?? new Set()
  group.add(capture); providers.set(key, group)
  return () => {group.delete(capture); if (!group.size) providers.delete(key)}
}
export function rememberSurface(source: string, host: HTMLElement, width: number, height: number): void {
  const original = host.querySelector('canvas, svg')
  if (!original) return
  try {
    original.dispatchEvent(new Event('blerg-chart-before-snapshot'))
    let drawing: Element, cost: number
    if (original instanceof HTMLCanvasElement) {
      const canvas = original.cloneNode(false) as HTMLCanvasElement
      const scale = Math.min(1, Math.sqrt(MAX_PIXELS / (original.width * original.height)))
      canvas.width = Math.max(1, Math.floor(original.width * scale))
      canvas.height = Math.max(1, Math.floor(original.height * scale))
      const context = canvas.getContext('2d')
      if (!context) throw new Error('CanvasUnavailable')
      context.drawImage(original, 0, 0, canvas.width, canvas.height)
      drawing = canvas
      cost = canvas.width * canvas.height
    } else {
      drawing = original.cloneNode(true) as Element
      cost = original.outerHTML.length * 2 + [...original.querySelectorAll('*')].reduce((bytes, el) => bytes + 96 + el.attributes.length * 48, 96)
    }
    const metadata = original.closest<HTMLElement>('[data-plot-bounds]')
    snapshots.set(source, {drawing, width, height, plot: metadata?.dataset.plotBounds, labels: metadata?.dataset.labelCount ?? String(original.querySelectorAll('text').length), legend: metadata?.dataset.legendCount ?? String(original.querySelectorAll('[data-legend-swatch]').length)}, drawing instanceof HTMLCanvasElement ? cost * 4 : cost)

  } catch (error) {
    console.warn('Chart transfer snapshot failed', {cause: error instanceof Error ? error.name : 'UnknownError'})
  }
}
export function transferSurface(source: string): HTMLElement | undefined {
  providers.get(sourceHash(source))?.values().next().value?.()
  const retained = snapshots.get(source)
  if (!retained) return
  const host = document.createElement('div')
  host.className = 'visual-scene visual-retained-snapshot'
  host.dataset.snapshotWidth = String(retained.width)
  host.dataset.snapshotHeight = String(retained.height)
  if (retained.plot) host.dataset.plotBounds = retained.plot
  host.dataset.labelCount = retained.labels
  host.dataset.legendCount = retained.legend
  host.setAttribute('aria-label', 'Retained chart snapshot')
  const drawing = retained.drawing.cloneNode(true) as Element
  if (retained.drawing instanceof HTMLCanvasElement) {
    const context = (drawing as HTMLCanvasElement).getContext('2d')
    if (!context) { console.warn('Chart transfer snapshot failed', {cause: 'canvas_unavailable'}); return }
    context.drawImage(retained.drawing, 0, 0)
  }
  if (drawing instanceof SVGSVGElement) {
    const prefix = `chart-transfer-${++snapshotId}-`
    const ids = new Map([...drawing.querySelectorAll('[id]'), ...(drawing.id ? [drawing] : [])].map(el => [el.id, prefix + el.id]))
    const rewrite = (value: string) => value.replace(/#([\w:-]+)/g, (match, id: string) => ids.has(id) ? '#' + ids.get(id)! : match)
    for (const el of [drawing, ...drawing.querySelectorAll('*')]) {
      for (const attr of [...el.attributes]) if (attr.name !== 'id') el.setAttribute(attr.name, rewrite(attr.value))
      if (el.id) el.id = ids.get(el.id)!
      if (el.localName === 'style') el.textContent = rewrite(el.textContent ?? '')
    }
  }

  host.append(drawing)
  return host
}
