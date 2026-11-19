import {BoundedCache} from './boundedCache'
import type {RenderedVisual} from './render'

// Display-only effects never alter the exported SVG attributes. Bound retained identities
// separately from chart models so theme/size rebuilds and tab transfers stay quiet.
const seenModels = new WeakSet<object>()
const seen = new BoundedCache<boolean>(8 << 20, 256)
export function entrance(root: SVGElement, kind: string, cached: boolean): () => void {
  const media = window.matchMedia?.('(prefers-reduced-motion: reduce)')
  if (cached || media?.matches) return () => {}
  const marks = kind === 'diagram' ? [root] : [...root.querySelectorAll<SVGElement>('[data-datum-key]')]
  const animations: Animation[] = []
  for (const [index, mark] of marks.entries()) {
    if (!mark.animate) continue
    const motion = mark.getAttribute('data-entrance') ?? kind
    let frames: Keyframe[] = [{opacity: 0}, {opacity: 1}]
    if (motion === 'bar') {
      const y = Number(mark.getAttribute('data-baseline') ?? (Number(mark.getAttribute('y')) + Number(mark.getAttribute('height'))))
      frames = [{transform: `translateY(${y}px) scaleY(0) translateY(${-y}px)`}, {transform: 'none'}]
    } else if (motion === 'line' || motion === 'area') {
      frames = [{clipPath: 'inset(0 100% 0 0)'}, {clipPath: 'inset(0 0 0 0)'}]
    } else if (motion === 'diagram') {
      frames = [{opacity: 0, translate: '0 6px'}, {opacity: 1, translate: '0 0'}]
    }
    animations.push(mark.animate(frames, {duration: 360, delay: Math.min(index * 12, 96), easing: 'cubic-bezier(.2,.7,.2,1)', fill: 'backwards'}))
  }
  const stop = () => { animations.forEach(animation => animation.cancel()); media?.removeEventListener?.('change', changed); root.removeEventListener('blerg-chart-before-snapshot', stop); root.removeEventListener('blerg-chart-finish-entrance', stop) }
  const changed = () => { if (media?.matches) stop() }
  media?.addEventListener?.('change', changed)
  root.addEventListener('blerg-chart-before-snapshot', stop)
  root.addEventListener('blerg-chart-finish-entrance', stop)
  return stop
}

export function polishVisual(visual: RenderedVisual, source: string, container: HTMLElement, kind: string, model?: object): RenderedVisual {
  const root = container.querySelector<SVGElement>('svg[data-chart-renderer="blerg"], svg[data-diagram-renderer]')
  if (!root) return visual
  const cached = !!seen.get(source) || !!model && seenModels.has(model)
  if (model) seenModels.add(model)
  seen.set(source, true, 8)
  const stop = entrance(root, kind, cached)
  const dispose = visual.dispose, resize = visual.resize, setTheme = visual.setTheme, applyData = visual.applyData
  visual.dispose = () => {stop(); dispose()}
  if (resize) visual.resize = (...args) => {stop(); resize(...args)}
  if (setTheme) visual.setTheme = (...args) => {stop(); setTheme(...args)}
  if (applyData) visual.applyData = (...args) => {stop(); const applied = applyData(...args); if (applied) seenModels.add(args[0]); return applied}
  return visual
}
