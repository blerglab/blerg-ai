// How many rendered lines a message's text must run before it offers to pop out into its own tab.
// Lines, not a share of the pane: the same message gets the same offer on a laptop and on a 4K
// screen. The person chooses it in Settings; the default is more than 4 lines.
import { useSyncExternalStore } from 'react'

export const POP_OUT_KEY = 'blerg.chat.popOutLines'
export const POP_OUT_CHOICES = [
  { value: 'always', label: 'Always', lines: 0 },
  { value: '4', label: '4 lines', lines: 4 },
  { value: '8', label: '8 lines', lines: 8 },
  { value: '12', label: '12 lines', lines: 12 },
  { value: '20', label: '20 lines', lines: 20 },
] as const
export type PopOutChoice = typeof POP_OUT_CHOICES[number]['value']
const DEFAULT: PopOutChoice = '4'
const EVENT = 'blerg-pop-out-pref'

export function readPopOutChoice(): PopOutChoice {
  try {
    const v = localStorage.getItem(POP_OUT_KEY)
    return POP_OUT_CHOICES.some(c => c.value === v) ? v as PopOutChoice : DEFAULT
  } catch {
    return DEFAULT
  }
}

export function writePopOutChoice(choice: PopOutChoice): void {
  try { localStorage.setItem(POP_OUT_KEY, choice) } catch { /* Private mode: lasts for this page. */ }
  window.dispatchEvent(new Event(EVENT))
}

/** The number of lines a message must run past before the pop-out shows. */
export function popOutLines(choice: PopOutChoice): number {
  return POP_OUT_CHOICES.find(c => c.value === choice)?.lines ?? 4
}

/** How many lines `body` takes as rendered: its height over its own line height, so wrapping,
 *  paragraph gaps, lists and code blocks all count by the space they take. */
export function renderedLines(body: HTMLElement): number {
  const style = body.ownerDocument.defaultView?.getComputedStyle(body)
  let line = Number.parseFloat(style?.lineHeight ?? '')
  if (!Number.isFinite(line) || line <= 0) {
    const font = Number.parseFloat(style?.fontSize ?? '')
    line = Number.isFinite(font) && font > 0 ? font * 1.2 : 20
  }
  return body.offsetHeight / line
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener(EVENT, onChange)
  window.addEventListener('storage', onChange)
  return () => { window.removeEventListener(EVENT, onChange); window.removeEventListener('storage', onChange) }
}

/** The person's choice, kept in step across every message and tab. */
export function usePopOutChoice(): PopOutChoice {
  return useSyncExternalStore(subscribe, readPopOutChoice, () => DEFAULT)
}
