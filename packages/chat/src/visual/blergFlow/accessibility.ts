// BLG-468 §4: hover and keyboard focus on nodes and edges, and chip activation — live DOM wired
// onto the SVG draw.ts already produced, never baked into the SVG markup itself (BLG-452's same
// discipline, accessibility.ts). Every node/edge group already carries tabindex="0" and an
// aria-label (draw.ts); this only adds the aria-live announcer and the event listeners, after the
// sanitized SVG is attached to a real document (listeners cannot attach to a detached parse).
export function wireFlowchartInteractivity(root: SVGSVGElement, onChipOpen?: (key: string) => void): HTMLElement {
  const live = document.createElement('div')
  live.setAttribute('aria-live', 'polite')
  live.className = 'visual-blerg-flow-live'
  live.style.cssText = 'position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)'

  const announce = (el: Element) => { live.textContent = el.getAttribute('aria-label') ?? '' }
  for (const el of root.querySelectorAll('[data-blerg-flow-node], [data-blerg-flow-edge]')) {
    el.addEventListener('focus', () => announce(el))
    el.addEventListener('mouseenter', () => announce(el))
    const chipKey = el.getAttribute('data-blerg-chip-key')
    if (!chipKey || !onChipOpen) continue
    el.addEventListener('click', () => onChipOpen(chipKey))
    el.addEventListener('keydown', event => {
      const key = (event as KeyboardEvent).key
      if (key === 'Enter' || key === ' ') { event.preventDefault(); onChipOpen(chipKey) }
    })
  }
  return live
}
