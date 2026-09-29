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
import { memo, useState, type ReactNode } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { safeUrl } from '../lib/safeUrl'
import './Markdown.css'


function textOf(node: ReactNode): string {
  if (node == null || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(textOf).join('')
  if (typeof node === 'object' && 'props' in node) {
    return textOf((node as { props: { children?: ReactNode } }).props.children)
  }
  return ''
}

function CodeBlock({ children }: { children?: ReactNode }) {
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
  pre({ children }) {
    return <CodeBlock>{children}</CodeBlock>
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

function MarkdownImpl({ text, className }: { text: string; className?: string }) {
  return (
    <div className={`md${className ? ` ${className}` : ''}`}>
      <ReactMarkdown remarkPlugins={[remarkGfm]} components={components} urlTransform={keepUrl}>
        {text}
      </ReactMarkdown>
    </div>
  )
}

/** Markdown renders `text` as safe, formatted Markdown. */
const Markdown = memo(MarkdownImpl)
export default Markdown
