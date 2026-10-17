// A fenced code block in the chat: copy (always the whole block), colour for the common
// languages, and a cap on length.
//
// A fence that has CLOSED is highlighted (lazily; plain text shows until the highlighter has
// loaded) and, over CAP_OVER lines, shows its first CAP_SHOW with a fade, "Show all N lines" and
// "Open" (CodeViewer). A fence still streaming is plain and uncapped: re-colouring it on every
// delta would cost a tokenizer run per delta, and capping it would make it jump.
import { useState, type ReactNode } from 'react'
import { type CodeLang } from '../model/codeLang'
import { useHighlight } from '../model/highlight'
import { CAP_OVER, CAP_SHOW, countLines, firstLines } from '../model/lineCap'
import CodeText from './CodeText'
import CodeViewer from './CodeViewer'

export function textOf(node: ReactNode): string {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(textOf).join('')
  if (typeof node === 'object' && 'props' in node) {
    return textOf((node as { props: { children?: ReactNode } }).props.children)
  }
  return ''
}

export default function CodeBlock({ children, note, lang = null, closed = false }: {
  children?: ReactNode
  note?: string
  lang?: CodeLang | null
  closed?: boolean
}) {
  const [copied, setCopied] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const [open, setOpen] = useState(false)
  const text = textOf(children).replace(/\n$/, '')
  const canCopy = typeof navigator !== 'undefined' && !!navigator.clipboard
  const total = countLines(text)
  const capped = closed && total > CAP_OVER
  const collapsed = capped && !expanded
  const shown = collapsed ? firstLines(text, CAP_SHOW) : text
  const highlighted = useHighlight(lang, shown, closed)
  return (
    <div className={`md-codeblock${collapsed ? ' is-capped' : ''}`}>
      {canCopy && (
        <button
          type="button"
          className="md-copy"
          aria-label="Copy code"
          onClick={() => {
            void navigator.clipboard.writeText(text).then(
              () => { setCopied(true); setTimeout(() => setCopied(false), 1200) },
              () => {},
            )
          }}
        >
          {copied ? 'Copied' : 'Copy'}
        </button>
      )}
      {closed ? (
        <pre data-lang={lang ?? undefined}>
          <code><CodeText text={shown} diff={lang === 'diff'} highlighted={highlighted} /></code>
        </pre>
      ) : (
        <pre>{children}</pre>
      )}
      {capped && (
        <div className="md-codeblock-actions">
          <button type="button" className="md-codeblock-more" aria-expanded={!collapsed} onClick={() => setExpanded(!expanded)}>
            {collapsed ? `Show all ${total} lines` : 'Show less'}
          </button>
          <button type="button" className="md-codeblock-more" onClick={() => setOpen(true)}>Open</button>
        </div>
      )}
      {open && <CodeViewer text={text} lang={lang} onClose={() => setOpen(false)} />}
      {note && <p className="md-card-note">{note}</p>}
    </div>
  )
}
