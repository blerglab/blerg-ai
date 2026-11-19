// One measurement delivery per observed body per layout pass, shared by chart surfaces.
const listeners = new Map<Element, () => void>()
let observer: ResizeObserver | undefined
export function observeSurface(node: Element, changed: () => void): () => void {
  if (typeof ResizeObserver === 'undefined') return () => {}
  observer ??= new ResizeObserver(entries => {
    for (const entry of entries) listeners.get(entry.target)?.()
  })
  listeners.set(node, changed)
  observer.observe(node)
  return () => {
    observer?.unobserve(node)
    listeners.delete(node)
    if (!listeners.size) { observer?.disconnect(); observer = undefined }
  }
}
