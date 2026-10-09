// Markdown renders model- and user-authored chat text as formatted Markdown
// (CommonMark + GFM tables/strikethrough/task lists/autolinks).
//
// Safety rules — this text comes from a model and from other people:
//   - No raw HTML, ever. react-markdown does not interpret HTML without
//     rehype-raw (deliberately not a dependency): an HTML fragment is shown as
//     the literal text it is, so `<script>` / `<img onerror>` never become
//     elements, and a model talking *about* HTML outside backticks still reads.
//   - Links: only http(s) and mailto survive (safeUrl); anything else —
//     javascript:, data:, vbscript:, relative paths — renders as plain text.
//     Kept links open in a new tab with rel="noopener noreferrer".
//   - Images are never loaded (a model-chosen URL is a tracking pixel waiting
//     to happen); they render as a link to the image instead.
//
// Streaming: the whole string is re-parsed on every delta. Partial Markdown is
// still valid Markdown (an unclosed ``` fence is a code block running to the
// end), so a half-written message renders sensibly and never throws.
//
// Cards: a ```card:<kind> fence whose body is a JSON object is handed to the
// card registry (cards/registry.ts) and drawn by the app's component for that
// kind. The fence is met at the `pre` element, where the code element carries
// the info string as its class; an unregistered kind stays the code block it
// is, a body that is not a JSON object gets the raw block and a note, and a
// fence that has not closed yet (still streaming) is a plain code block —
// its JSON is never parsed, complete as it may look.
import { createContext, isValidElement, memo, useContext, useState, type ReactNode } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { safeUrl } from '../model/safeUrl'
import { cardKind, parseCardFence } from '../cards/fence'
import type { CardRegistry } from '../cards/registry'
import './markdown.css'

function textOf(node: ReactNode): string {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(textOf).join('')
  if (typeof node === 'object' && 'props' in node) {
    return textOf((node as { props: { children?: ReactNode } }).props.children)
  }
  return ''
}

function CodeBlock({ children, note }: { children?: ReactNode; note?: string }) {
  const [copied, setCopied] = useState(false)
  const text = textOf(children).replace(/\n$/, '')
  const canCopy = typeof navigator !== 'undefined' && !!navigator.clipboard
  return (
    <div className="md-codeblock">
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
      <pre>{children}</pre>
      {note && <p className="md-card-note">{note}</p>}
    </div>
  )
}

// What a fence needs from the surrounding render: the registry, and the source
// text to tell a closed fence from one still streaming. A context rather than
// props so the component table stays one module-level constant and the tree
// is not remounted on every delta.
interface FenceContext {
  cards?: CardRegistry
  source: string
}
const Fence = createContext<FenceContext>({ source: '' })

// The code element inside a pre, as react-markdown renders it: its class is
// the fence's info string, its children the body.
function codeOf(children: ReactNode): { className?: string; text: string } | null {
  const only = Array.isArray(children) && children.length === 1 ? children[0] : children
  if (!isValidElement<{ className?: string; children?: ReactNode }>(only)) return null
  return { className: only.props.className, text: textOf(only.props.children) }
}

// A fence is closed when the source it spans ends in its closing line. The
// position is the block's own, so the closing line is the last one of the
// span; an unclosed fence runs to the end of the text with no such line.
type Position = { start: { offset?: number }; end: { offset?: number } }

function fenceClosed(source: string, position: Position | undefined): boolean {
  if (!position || position.start.offset === undefined || position.end.offset === undefined) return false
  const raw = source.slice(position.start.offset, position.end.offset)
  const lines = raw.split('\n')
  return lines.length > 1 && /^ {0,3}(`{3,}|~{3,})\s*$/.test(lines[lines.length - 1])
}

// The fenced block: a card when the registry knows its kind and the fence is
// complete and well-formed, else the code block it is.
function FencedBlock({ children, position }: { children?: ReactNode; position?: Position }) {
  const { cards, source } = useContext(Fence)
  const code = cards ? codeOf(children) : null
  const kind = code ? cardKind(code.className) : null
  if (!code || kind === null || !fenceClosed(source, position)) return <CodeBlock>{children}</CodeBlock>
  const def = cards?.get(kind)
  if (!def) return <CodeBlock>{children}</CodeBlock>
  const fence = parseCardFence(code.className, code.text.replace(/\n$/, ''))
  if (!fence) return <CodeBlock note={`This ${kind} card did not parse: its body is not a JSON object.`}>{children}</CodeBlock>
  const { Component } = def
  return (
    <div className="md-card" data-card-kind={fence.kind}>
      <Component kind={fence.kind} data={fence.data} />
    </div>
  )
}

const components: Components = {
  a({ href, children }) {
    const url = safeUrl(href)
    if (!url) return <span className="md-link-inert">{children}</span>
    return (
      // stopPropagation: chat cards are often clickable themselves, and
      // following a link must not also navigate the card.
      <a href={url} target="_blank" rel="noopener noreferrer" onClick={e => e.stopPropagation()}>
        {children}
      </a>
    )
  },
  img({ src, alt }) {
    const url = safeUrl(typeof src === 'string' ? src : null)
    const label = alt || 'image'
    if (!url) return <span className="md-link-inert">[{label}]</span>
    return (
      <a href={url} target="_blank" rel="noopener noreferrer" onClick={e => e.stopPropagation()}>
        [{label}]
      </a>
    )
  },
  pre({ children, node }) {
    return <FencedBlock position={node?.position}>{children}</FencedBlock>
  },
  table({ children }) {
    return (
      <div className="md-table-wrap">
        <table>{children}</table>
      </div>
    )
  },
}

// Links are vetted in the `a` component above; the transform only has to
// keep react-markdown from rewriting them first.
const keepUrl = (url: string) => url

export interface MarkdownProps {
  text: string
  className?: string
  /** The app's cards; without one every card fence is a code block. */
  cards?: CardRegistry
}

function MarkdownImpl({ text, className, cards }: MarkdownProps) {
  return (
    <div className={`md${className ? ` ${className}` : ''}`}>
      <Fence.Provider value={{ cards, source: text }}>
        <ReactMarkdown remarkPlugins={[remarkGfm]} components={components} urlTransform={keepUrl}>
          {text}
        </ReactMarkdown>
      </Fence.Provider>
    </div>
  )
}

/** Markdown renders `text` as safe, formatted Markdown, with the app's cards in place of card fences. */
const Markdown = memo(MarkdownImpl)
export default Markdown
