// The Copy button on a message, as in Claude.ai and ChatGPT: it shows on hover or keyboard focus
// (a touch screen has long-press instead, see LongPress) and copies the message as its Markdown source (text/plain) beside the
// rendered message as sanitised HTML (text/html), so a paste into a rich editor keeps the
// formatting and a paste into a plain one gets the Markdown.
import { useRef, useState } from 'react'
import { useCopySource } from './LongPress'
import { copyPayload } from '../model/richClipboard'
import { writeRich } from '../model/richCopy'

export default function CopyMessage({ text }: { text: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  const btn = useRef<HTMLButtonElement>(null)
  useCopySource(btn, '.agent-card', text)
  const label = state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed' : 'Copy message'
  return (
    <button
      ref={btn}
      type="button"
      className={`msg-copy${state === 'idle' ? '' : ` is-${state}`}`}
      aria-label={label}
      title={label}
      data-nocopy=""
      onClick={e => {
        const md = e.currentTarget.closest('.agent-card')?.querySelector('.md')
        let html = ''
        try { html = md ? copyPayload(md).html : '' } catch { /* the source still copies */ }
        void writeRich(text, html).then(ok => {
          setState(ok ? 'copied' : 'failed')
          clearTimeout(timer.current)
          timer.current = setTimeout(() => setState('idle'), 1500)
        })
      }}
    >
      <span className={`msg-copy-icon${state === 'copied' ? ' done' : ''}`} aria-hidden="true" />
      <span className="msg-copy-status" aria-live="polite">{state === 'copied' ? 'Copied' : ''}</span>
    </button>
  )
}
