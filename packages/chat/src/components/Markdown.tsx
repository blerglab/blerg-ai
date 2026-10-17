// Markdown renders model- and user-authored chat text as formatted Markdown
// (CommonMark + GFM tables/strikethrough/task lists/autolinks).
//
// Safety rules — this text comes from a model and from other people:
//   - No raw HTML, ever. react-markdown does not interpret HTML without
//     rehype-raw (deliberately not a dependency): an HTML fragment is shown as
//     the literal text it is, so `<script>` / `<img onerror>` never become
//     elements, and a model talking *about* HTML outside backticks still reads.
//   - Links: only http(s) and mailto survive (safeUrl); anything else —
//     javascript:, data:, vbscript:, unrecognized relative paths — renders as plain text.
//     Known published artifact links open the chat viewer on a normal click; other kept
//     links and modified clicks open a new tab with rel="noopener noreferrer".
//   - A relative link or path-like inline code in AGENT text is a button that asks the host to open
//     the file from the session's folder (workspaceLinks); no host, no change.
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
import { createContext, isValidElement, memo, useContext, type ReactNode } from 'react'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { safeUrl } from '../model/safeUrl'
import { workspaceFromCode, workspaceFromHref } from '../model/workspacePaths'
import { cardKind, parseCardFence } from '../cards/fence'
import type { CardRegistry } from '../cards/registry'
import { langFromInfo } from '../model/codeLang'
import './markdown.css'
import CodeBlock, { textOf } from './CodeBlock'
import { useChat } from './context'

// What a fence needs from the surrounding render: the registry, and the source
// text to tell a closed fence from one still streaming. A context rather than
// props so the component table stays one module-level constant and the tree
// is not remounted on every delta.
interface FenceContext {
  cards?: CardRegistry
  source: string
  /** Agent text on a host that opens session files: paths in it are links. */
  workspace?: boolean
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
type Position = { start: { offset?: number; line?: number }; end: { offset?: number; line?: number } }

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
  const code = codeOf(children)
  const closed = fenceClosed(source, position)
  const lang = langFromInfo(code?.className)
  const plain = <CodeBlock lang={lang} closed={closed}>{children}</CodeBlock>
  const kind = cards && code ? cardKind(code.className) : null
  if (!cards || !code || kind === null || !closed) return plain
  const def = cards.get(kind)
  if (!def) return plain
  const fence = parseCardFence(code.className, code.text.replace(/\n$/, ''))
  if (!fence) return <CodeBlock lang={null} closed={closed} note={`This ${kind} card did not parse: its body is not a JSON object.`}>{children}</CodeBlock>
  const { Component } = def
  return (
    <div className="md-card" data-card-kind={fence.kind}>
      <Component kind={fence.kind} data={fence.data} />
    </div>
  )
}

const WS_TITLE = 'Open from the session’s folder'

// Opens on Enter and Space like a button; stopPropagation as every link in a clickable card does.
function wsProps(open: () => void) {
  return {
    role: 'button' as const,
    tabIndex: 0,
    title: WS_TITLE,
    onClick: (e: React.MouseEvent) => { e.stopPropagation(); open() },
    onKeyDown: (e: React.KeyboardEvent) => {
      if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); e.stopPropagation(); open() }
    },
  }
}

function MarkdownLink({ href, children }: { href?: string; children?: ReactNode }) {
  const chat = useChat()
  const { workspace } = useContext(Fence)
  const artifact = chat?.artifactLink?.(href)
  const url = artifact?.href ?? safeUrl(href)
  if (!url) {
    const ref = workspace && chat?.openWorkspaceFile ? workspaceFromHref(href) : null
    if (ref && chat?.openWorkspaceFile) {
      const open = chat.openWorkspaceFile
      return <span className="md-wsfile md-wslink" data-workspace-path={ref.path} {...wsProps(() => open(ref.path, ref.line))}>{children}</span>
    }
    return <span className="md-link-inert">{children}</span>
  }
  return (
    // stopPropagation: chat cards are often clickable themselves, and
    // following a link must not also navigate the card.
    <a href={url} target="_blank" rel="noopener noreferrer" onClick={e => {
      e.stopPropagation()
      if (artifact && !e.defaultPrevented && e.button === 0 && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey) {
        e.preventDefault()
        chat?.view(artifact.artifact)
      }
    }}>
      {children}
    </a>
  )
}

// Inline code is a file link when it looks like a path, the host opens session files, and the
// text is agent-authored. A code block's own code element has a language class or spans lines.
function InlineCode({ className, children, node }: { className?: string; children?: ReactNode; node?: { position?: Position } }) {
  const chat = useChat()
  const { workspace } = useContext(Fence)
  const pos = node?.position
  const oneLine = !!pos && pos.start.line !== undefined && pos.start.line === pos.end.line
  const text = typeof children === 'string' ? children : Array.isArray(children) && children.every(c => typeof c === 'string') ? children.join('') : null
  const ref = workspace && chat?.openWorkspaceFile && !className && oneLine && text !== null ? workspaceFromCode(text) : null
  if (ref && chat?.openWorkspaceFile) {
    const open = chat.openWorkspaceFile
    return <code className="md-wsfile" data-workspace-path={ref.path} {...wsProps(() => open(ref.path, ref.line))}>{children}</code>
  }
  return <code className={className}>{children}</code>
}

const components: Components = {
  a: MarkdownLink,
  code: InlineCode,
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
  /** Agent-authored text: relative links and path-like inline code open the file from the session's
   *  folder, when the host offers it (ChatView's canOpenWorkspaceFiles). Never for a person's text. */
  workspaceLinks?: boolean
}

function MarkdownImpl({ text, className, cards, workspaceLinks }: MarkdownProps) {
  return (
    <div className={`md${className ? ` ${className}` : ''}`}>
      <Fence.Provider value={{ cards, source: text, workspace: workspaceLinks }}>
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
