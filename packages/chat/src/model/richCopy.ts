// Copying out of the chat keeps structure. A copy whose selection lies in rendered Markdown puts
// the selection beside itself as Markdown (text/plain) and as sanitised HTML (text/html); a
// selection inside one code block puts the code text exactly. Anything else, and any failure,
// leaves the browser's own copy alone.
import { codePayload, copyPayload } from './richClipboard'

function execCopy(text: string): boolean {
  const box = document.createElement('textarea')
  box.value = text
  box.setAttribute('readonly', '')
  box.style.cssText = 'position:fixed;top:0;left:0;opacity:0;pointer-events:none'
  const keep = document.activeElement instanceof HTMLElement ? document.activeElement : null
  document.body.appendChild(box)
  try {
    box.select()
    return typeof document.execCommand === 'function' && document.execCommand('copy')
  } catch {
    return false
  } finally {
    box.remove()
    keep?.focus({ preventScroll: true })
  }
}

/** Writes both flavours to the clipboard (text/plain only when there is no HTML); on a browser that
 *  will not take them, the Markdown alone by writeText, then by a hidden textarea. False if all fail. */
export async function writeRich(markdown: string, html: string): Promise<boolean> {
  const clip = typeof navigator !== 'undefined' ? navigator.clipboard : undefined
  try {
    if (typeof ClipboardItem !== 'undefined' && clip?.write) {
      const item: Record<string, Blob> = { 'text/plain': new Blob([markdown], { type: 'text/plain' }) }
      if (html) item['text/html'] = new Blob([html], { type: 'text/html' })
      await clip.write([new ClipboardItem(item)])
      return true
    }
  } catch { /* try the plain way */ }
  try {
    if (clip?.writeText) {
      await clip.writeText(markdown)
      return true
    }
  } catch { /* try the textarea */ }
  return execCopy(markdown)
}

const elementOf = (n: Node): Element | null => (n.nodeType === 1 ? (n as Element) : n.parentElement)

/** The copy handler for a chat root. Call from a `copy` event on or above the root. */
export function handleChatCopy(e: ClipboardEvent, root: Element): void {
  try {
    const target = e.target
    if (target instanceof HTMLInputElement || target instanceof HTMLTextAreaElement) return
    const sel = window.getSelection()
    if (!sel || sel.isCollapsed || sel.rangeCount === 0 || !e.clipboardData) return
    const range = sel.getRangeAt(0)
    const anc = elementOf(range.commonAncestorContainer)
    if (!anc || !root.contains(anc)) return
    const pre = anc.closest('pre')
    let payload: { markdown: string; html: string }
    if (pre && root.contains(pre) && pre.closest('.md')) {
      payload = codePayload(sel.toString(), pre.getAttribute('data-lang') ?? '')
    } else {
      const holder = document.createElement('div')
      holder.appendChild(range.cloneContents())
      if (!anc.closest('.md') && !holder.querySelector('.md')) return
      payload = copyPayload(holder)
    }
    if (!payload.markdown.trim()) return
    e.clipboardData.setData('text/plain', payload.markdown)
    e.clipboardData.setData('text/html', payload.html)
    e.preventDefault()
  } catch {
    // The browser's own copy goes ahead.
  }
}
