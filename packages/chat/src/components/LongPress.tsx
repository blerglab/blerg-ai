// Long-press to copy on a touch screen, as in phone chat apps. The Copy buttons are hidden there;
// holding a message, code block, ticket card or checklist row opens a small sheet at the bottom:
// Copy message, Select text, Cancel. One delegated listener on the timeline serves every card.
// A press that starts on a link or a button is left to the browser. Nothing under the finger can
// be selected while it is down (the browser's own long-press would select the sheet's button), and
// Select text opens the text in a read-only box, where every browser gives its own handles.
import { useCallback, useEffect, useRef, useState, type RefObject } from 'react'
import { copyPayload } from '../model/richClipboard'
import { writeRich } from '../model/richCopy'

export const HOLD_MS = 500
export const SLOP_PX = 10

// What a card copies, set by the component that owns the source text.
const sources = new WeakMap<Element, () => string>()
export function useCopySource(ref: RefObject<Element | null>, closest: string | null, text: string) {
  useEffect(() => {
    const el = ref.current
    const target = el && (closest ? el.closest(closest) : el)
    if (!target) return
    sources.set(target, () => text)
    return () => { sources.delete(target) }
  }, [ref, closest, text])
}

interface Target {
  text: string
  html: string
  /** What Select text shows: the message as read (no Markdown marks), or the code as is. */
  plain: string
}

const CARDS = '.md-codeblock, .ticket-card, .checklist-row, .agent-card.assistant, .agent-card.user'

// The text as the person reads it. jsdom has no innerText; textContent is close enough there.
const readText = (el: Element) => ((el as HTMLElement).innerText ?? el.textContent ?? '').trim()

function payloadOf(card: Element): Omit<Target, 'plain'> & { plain?: string } | null {
  const src = sources.get(card)
  if (card.matches('.agent-card.assistant, .agent-card.user')) {
    if (!src) return null
    const md = card.querySelector('.md')
    let html = ''
    try { html = md ? copyPayload(md).html : '' } catch { /* the source still copies */ }
    return { text: src(), html, plain: md ? readText(md) : undefined }
  }
  if (card.matches('.md-codeblock')) return src ? { text: src(), html: '' } : null
  if (card.matches('.ticket-card')) {
    const key = card.getAttribute('data-ticket-key') ?? ''
    const title = card.querySelector('.ticket-title')?.textContent ?? ''
    const url = card.querySelector('a.ticket-key')?.getAttribute('href') ?? ''
    const text = [`${key} ${title}`.trim(), url].filter(Boolean).join('\n')
    return text ? { text, html: '' } : null
  }
  const items = [...card.querySelectorAll('.checklist-items > li')]
  if (items.length > 0) {
    const text = items.map(li => `- [${li.classList.contains('done') ? 'x' : ' '}] ${li.querySelector('.checklist-text')?.textContent ?? ''}`).join('\n')
    return { text, html: '' }
  }
  const text = (card.textContent ?? '').trim()
  return text ? { text, html: '' } : null
}

const isTouch = () => typeof window.matchMedia === 'function' && window.matchMedia('(hover: none)').matches

export function useLongPress(root: RefObject<HTMLElement | null>): { sheet: Target | null; close: () => void } {
  const [sheet, setSheet] = useState<Target | null>(null)
  const close = useCallback(() => setSheet(null), [])
  useEffect(() => {
    const el = root.current
    if (!el) return
    let timer: ReturnType<typeof setTimeout> | undefined
    let start: { x: number; y: number } | null = null
    let opened = false
    const cancel = () => { clearTimeout(timer); timer = undefined; start = null; el.removeAttribute('data-pressing') }
    const down = (e: TouchEvent) => {
      cancel()
      opened = false
      if (e.touches.length !== 1 || !isTouch()) return
      const t = e.target instanceof Element ? e.target : null
      if (!t || t.closest('a, button, input, textarea, select, [data-nocopy]')) return
      const card = t.closest(CARDS)
      if (!card) return
      const { clientX: x, clientY: y } = e.touches[0]
      start = { x, y }
      el.setAttribute('data-pressing', '')
      timer = setTimeout(() => {
        timer = undefined
        const p = payloadOf(card)
        if (!p) { cancel(); return }
        // data-pressing stays until the finger lifts: the card stays unselectable meanwhile.
        opened = true
        clearSelection()
        navigator.vibrate?.(10)
        setSheet({ text: p.text, html: p.html, plain: p.plain || p.text })
      }, HOLD_MS)
    }
    const move = (e: TouchEvent) => {
      if (!start) return
      const t = e.touches[0]
      if (Math.hypot(t.clientX - start.x, t.clientY - start.y) > SLOP_PX) cancel()
    }
    // The browser's own menu follows a long press; ours replaces it.
    const menu = (e: Event) => { if (opened || start) e.preventDefault() }
    // A selection the browser made under the held finger goes when it lifts.
    const up = () => { if (opened) clearSelection(); cancel() }
    el.addEventListener('touchstart', down, { passive: true })
    el.addEventListener('touchmove', move, { passive: true })
    el.addEventListener('touchend', up)
    el.addEventListener('touchcancel', cancel)
    el.addEventListener('scroll', cancel, { passive: true })
    el.addEventListener('contextmenu', menu)
    return () => {
      cancel()
      el.removeEventListener('touchstart', down)
      el.removeEventListener('touchmove', move)
      el.removeEventListener('touchend', up)
      el.removeEventListener('touchcancel', cancel)
      el.removeEventListener('scroll', cancel)
      el.removeEventListener('contextmenu', menu)
    }
  }, [root])
  return { sheet, close }
}

const clearSelection = () => { window.getSelection()?.removeAllRanges() }

export function LongPressSheet({ target, onClose }: { target: Target; onClose: () => void }) {
  const first = useRef<HTMLButtonElement>(null)
  const [done, setDone] = useState<'copied' | 'failed' | null>(null)
  const [selecting, setSelecting] = useState(false)
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null
    first.current?.focus()
    return () => { prev?.focus?.() }
  }, [])
  useEffect(() => {
    if (!done) return
    const t = setTimeout(onClose, 900)
    return () => clearTimeout(t)
  }, [done, onClose])
  if (selecting) return <SelectText text={target.plain} onClose={onClose} />
  return (
    <div className="lp-backdrop" onClick={onClose} data-testid="lp-backdrop">
      <div
        className="lp-sheet"
        role="dialog"
        aria-modal="true"
        aria-label="Message actions"
        onClick={e => e.stopPropagation()}
        onKeyDown={e => { if (e.key === 'Escape') onClose() }}
      >
        {done ? (
          <div className="lp-toast" role="status">{done === 'copied' ? 'Copied' : 'Copy failed'}</div>
        ) : (
          <>
            <button type="button" ref={first} onClick={() => { void writeRich(target.text, target.html).then(ok => setDone(ok ? 'copied' : 'failed')) }}>Copy message</button>
            <button type="button" onClick={() => setSelecting(true)}>Select text</button>
            <button type="button" className="lp-cancel" onClick={onClose}>Cancel</button>
          </>
        )}
      </div>
    </div>
  )
}

/** Select text: the text full screen in a read-only box, all of it selected so the browser's handles
 *  show at once; drag them in, then Copy takes the selection (or everything when it is empty). */
function SelectText({ text, onClose }: { text: string; onClose: () => void }) {
  const box = useRef<HTMLTextAreaElement>(null)
  const [done, setDone] = useState<'copied' | 'failed' | null>(null)
  useEffect(() => {
    const el = box.current
    if (!el) return
    el.focus({ preventScroll: true })
    el.setSelectionRange(0, el.value.length)
  }, [])
  useEffect(() => {
    if (!done) return
    const t = setTimeout(onClose, 900)
    return () => clearTimeout(t)
  }, [done, onClose])
  const copy = () => {
    const el = box.current
    const part = el && el.selectionEnd > el.selectionStart ? el.value.slice(el.selectionStart, el.selectionEnd) : text
    void writeRich(part, '').then(ok => setDone(ok ? 'copied' : 'failed'))
  }
  return (
    <div
      className="lp-select"
      role="dialog"
      aria-modal="true"
      aria-label="Select text"
      onKeyDown={e => { if (e.key === 'Escape') onClose() }}
    >
      <div className="lp-select-bar">
        <span className="lp-select-title">{done ? (done === 'copied' ? 'Copied' : 'Copy failed') : 'Select text'}</span>
        <button type="button" onClick={copy}>Copy</button>
        <button type="button" className="lp-select-done" onClick={onClose}>Done</button>
      </div>
      <textarea ref={box} className="lp-select-text" readOnly value={text} aria-label="Text to select" />
    </div>
  )
}
