// scrollToCall: brings the row that draws tool call callId into view and flashes it — the jump a
// background task or a running subagent makes to the call that started it. The row is the call's
// own row or card, else the folded group that holds it. scrollToEvent does the same for a card
// drawn for one event (an ask). Nothing happens when the row is not drawn (it is above the rows the
// transcript has drawn so far). Both return whether they found the row.

/** How long the row a jump lands on stays highlighted. */
export const FLASH_MS = 1600

function escape(id: string): string {
  return typeof CSS !== 'undefined' && typeof CSS.escape === 'function' ? CSS.escape(id) : id.replace(/["\\]/g, '\\$&')
}

function flash(el: Element | null): boolean {
  if (!el) return false
  el.scrollIntoView?.({ block: 'center', behavior: 'smooth' })
  el.classList.add('bg-flash')
  setTimeout(() => el.classList.remove('bg-flash'), FLASH_MS)
  return true
}

export function scrollToCall(callId: string, root: ParentNode = document): boolean {
  if (!callId) return false
  const id = escape(callId)
  return flash(root.querySelector(`[data-call-id="${id}"]`) ?? root.querySelector(`[data-call-ids~="${id}"]`))
}

export function scrollToEvent(eventId: string, root: ParentNode = document): boolean {
  if (!eventId) return false
  return flash(root.querySelector(`[data-event-id="${escape(eventId)}"]`))
}
