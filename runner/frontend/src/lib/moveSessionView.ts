// Reparenting with append resets scroll and iframe documents. Prefer an atomic DOM move;
// older browsers and a pruned source group still need append plus saved scroll offsets.
type Scroll = { element: HTMLElement; top: number; left: number }
const saved = new WeakMap<HTMLElement, Scroll[]>()
export function rememberViewScroll(target: HTMLElement) {
  if (!target.isConnected || target.hidden) return
  saved.set(target, [target, ...target.querySelectorAll<HTMLElement>('*')]
    .filter(element => element.scrollTop || element.scrollLeft)
    .map(element => ({ element, top: element.scrollTop, left: element.scrollLeft })))
}
export function moveSessionView(parent: HTMLElement, target: HTMLElement) {
  if (target.parentElement === parent) return
  rememberViewScroll(target)
  const atomicParent = parent as HTMLElement & { moveBefore?: (node: Node, child: Node | null) => void }
  if (atomicParent.moveBefore && target.isConnected && parent.isConnected) atomicParent.moveBefore(target, null)
  else parent.append(target)
  for (const { element, top, left } of saved.get(target) ?? []) {
    element.scrollTop = top; element.scrollLeft = left
  }
}
