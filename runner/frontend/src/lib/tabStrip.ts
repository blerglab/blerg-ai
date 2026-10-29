/** Only the tab strip moves: revealing a tab must never scroll the session or its viewer. */
export function tabScrollDelta(viewport: { left: number; right: number }, tab: { left: number; right: number }) {
  return tab.left < viewport.left ? tab.left - viewport.left : tab.right > viewport.right ? tab.right - viewport.right : 0
}
export function tabWheelDelta(event: Pick<WheelEvent, 'deltaX' | 'deltaY' | 'deltaMode' | 'ctrlKey'>, width: number) {
  if (event.ctrlKey) return 0 // Preserve browser zoom and trackpad pinch.
  return (Math.abs(event.deltaX) >= Math.abs(event.deltaY) ? event.deltaX : event.deltaY) * (event.deltaMode === 1 ? 16 : event.deltaMode === 2 ? width : 1)
}
export function tabDragSpeed(x: number, left: number, right: number) {
  const edge = Math.min(40, (right - left) / 3)
  if (edge <= 0) return 0
  return x < left + edge ? -900 * Math.min(1, (left + edge - x) / edge) : x > right - edge ? 900 * Math.min(1, (x - right + edge) / edge) : 0
}
