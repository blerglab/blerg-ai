// BLG-471 §4/ticket: hover and keyboard focus on a task bar announces its name/dates/duration
// (draw.ts already set `aria-label` to that text), highlights the bars and connector lines for
// its own `after` dependencies, and activates a card-key chip — the same live-DOM-wired-onto-
// already-drawn-SVG discipline as blergFlow/accessibility.ts and blergSequence/accessibility.ts.
export function wireGanttInteractivity(root: SVGSVGElement, onChipOpen?: (key: string) => void): HTMLElement {
  const live = document.createElement('div')
  live.setAttribute('aria-live', 'polite')
  live.className = 'visual-blerg-gantt-live'
  live.style.cssText = 'position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)'

  const announce = (el: Element) => { live.textContent = el.getAttribute('aria-label') ?? '' }

  function setHighlight(taskId: string, on: boolean): void {
    const depends = (root.querySelector(`[data-blerg-gantt-task="${taskId}"]`)?.getAttribute('data-blerg-gantt-depends') ?? '')
      .split(',').filter(Boolean)
    for (const depId of depends) {
      const shape = root.querySelector(`[data-blerg-gantt-bar-shape="${depId}"]`)
      if (shape) shape.setAttribute('stroke', shape.getAttribute(on ? 'data-blerg-gantt-stroke-highlight' : 'data-blerg-gantt-stroke-default') ?? '')
      const depLine = root.querySelector(`[data-blerg-gantt-dep-line="${depId}->${taskId}"]`)
      if (depLine) depLine.setAttribute('opacity', on ? '1' : '0')
    }
  }

  for (const el of root.querySelectorAll('[data-blerg-gantt-task]')) {
    const taskId = el.getAttribute('data-blerg-gantt-task') ?? ''
    el.addEventListener('focus', () => { announce(el); setHighlight(taskId, true) })
    el.addEventListener('blur', () => setHighlight(taskId, false))
    el.addEventListener('mouseenter', () => { announce(el); setHighlight(taskId, true) })
    el.addEventListener('mouseleave', () => setHighlight(taskId, false))
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
