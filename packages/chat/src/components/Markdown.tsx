// Markdown renders model- and user-authored chat text as formatted Markdown
// (CommonMark + GFM tables/strikethrough/task lists/autolinks).
//
// Safety rules — this text comes from a model and from other people:
//   - Raw HTML is literal text by default (react-markdown shows it as the text it is). A document
//     surface opts in with `html`: then only GitHub's allowlist of elements and attributes,
//     sanitized by DOMPurify and rebuilt as React elements (model/markdownHtml.ts); `<script>`,
//     `<img onerror>`, `<iframe>`, `<style>`, event handlers and style attributes never reach the page.
//   - Links: only http(s) and mailto survive (safeUrl); anything else —
//     javascript:, data:, vbscript:, unrecognized relative paths — renders as plain text.
//     Known published artifact links open the chat viewer on a normal click; other kept
//     links and modified clicks open a new tab with rel="noopener noreferrer". A `#fragment`
//     scrolls to the heading inside this block.
//   - A relative link or path-like inline code in AGENT text is a button that asks the host to open
//     the file from the session's folder (workspaceLinks); no host, no change. In a DOCUMENT (the
//     `document` prop: the viewer, the review page) a relative link resolves against the file's own
//     path and opens inside Blerg.
//   - Images in chat text are never loaded (a model-chosen URL is a tracking pixel waiting to
//     happen); they render as a link to the image instead. In a document, a relative image loads
//     from the session's folder through the workspace-file read (when the document has one; through
//     the page's image queue, two at a time, at most MAX_DOC_IMAGES before "Load images"), a remote
//     image only when the person clicks "Load image", a base64 raster data image as it is. A
//     published or uploaded document has no folder: its relative images and links stay text.
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
import { createContext, isValidElement, memo, useContext, useEffect, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { safeUrl } from '../model/safeUrl'
import { workspaceFromCode, workspaceFromHref } from '../model/workspacePaths'
import { cardKind, parseCardFence } from '../cards/fence'
import type { CardRegistry } from '../cards/registry'
import { langFromInfo } from '../model/codeLang'
import { classifyUrl, HEADING_ID_PREFIX, imageBlobType, MAX_DOC_IMAGES, rehypeHeadingIds, rehypeImageIndex, rehypeSafeHtml, resolveDocPath } from '../model/markdownHtml'
import { queuedRead } from '../model/imageQueue'
import { isSvgImage, mediaUrl, releaseMediaUrl } from '../model/artifactViews'
import type { WorkspaceFile } from '../transport/types'
import './markdown.css'
import CodeBlock, { textOf } from './CodeBlock'
import VisualBlock from './VisualBlock'
import { visualFence } from '../model/visualKind'
import { useChat } from './context'
import { RefSpanFor, rehypeRefs, useRefContext } from './RefLinks'

// What a fence needs from the surrounding render: the registry, and the source
// text to tell a closed fence from one still streaming. A context rather than
// props so the component table stays one module-level constant and the tree
// is not remounted on every delta.
interface FenceContext {
  cards?: CardRegistry
  source: string
  /** Agent text on a host that opens session files: paths in it are links. */
  workspace?: boolean
  /** A document (the viewer, the review page), with where it came from. */
  doc?: MarkdownDocument
  /** The images past MAX_DOC_IMAGES were asked for. */
  allImages?: boolean
  showAllImages?: () => void
  /** Card and commit references are marked in this text (chat text in a chat that lends them). */
  refs?: boolean
}

// The credit for the Markdown styling (markdown.css), kept by the minifier as a legal comment.
/*! @license @blerglab/chat Markdown styling adapted from github-markdown-css, MIT License, Copyright (c) Sindre Sorhus <sindresorhus@gmail.com> (https://sindresorhus.com). See THIRD_PARTY_NOTICES.md. */
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
  const visual = visualFence(code?.className)
  if (code && closed && visual) return <VisualBlock kind={visual} source={code.text.replace(/\n$/, '')} />
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

// `#fragment`: the heading with that GitHub id inside this Markdown block, scrolled into view.
function scrollToFragment(from: HTMLElement, href: string) {
  let frag: string
  try { frag = decodeURIComponent(href.slice(1)) } catch { return }
  const id = HEADING_ID_PREFIX + frag.toLowerCase()
  const root = from.closest('.md')
  const target = root ? Array.from(root.querySelectorAll('[id]')).find(el => el.id === id) : undefined
  target?.scrollIntoView({ block: 'start', behavior: 'smooth' })
}

// A control an image inside a link needs (Retry) is drawn just after the link, never inside it: a
// button inside an <a> is invalid and reads to a screen reader as a button in a link.
const AfterLink = createContext<{ inLink: boolean; slot: HTMLElement | null }>({ inLink: false, slot: null })

function MarkdownLink(props: { href?: string; children?: ReactNode }) {
  const [slot, setSlot] = useState<HTMLSpanElement | null>(null)
  return (
    <>
      <MarkdownLinkInner href={props.href}><AfterLink.Provider value={{ inLink: true, slot }}>{props.children}</AfterLink.Provider></MarkdownLinkInner>
      <span ref={setSlot} className="md-after-link" />
    </>
  )
}

function MarkdownLinkInner({ href, children }: { href?: string; children?: ReactNode }) {
  const chat = useChat()
  const { workspace, doc } = useContext(Fence)
  if (href && href.startsWith('#') && href.length > 1) {
    return <a href={href} className="md-anchor" onClick={e => { e.preventDefault(); e.stopPropagation(); scrollToFragment(e.currentTarget, href) }}>{children}</a>
  }
  const artifact = chat?.artifactLink?.(href)
  const url = artifact?.href ?? safeUrl(href)
  if (!url) {
    // A folder file's relative link names the file beside it, in the folder; agent text's names one
    // in the folder. A published or uploaded document has no folder: its relative links stay text.
    const relative = doc && href ? classifyUrl(href, 'href')?.kind === 'relative' : false
    const docPath = relative && doc?.readWorkspace ? resolveDocPath(doc.path, href!) : null
    const ref = !chat?.openWorkspaceFile ? null : docPath ? workspaceFromHref(docPath) : workspace && !doc ? workspaceFromHref(href) : null
    if (ref && chat?.openWorkspaceFile) {
      const open = chat.openWorkspaceFile
      const folder = !!docPath // the folder's file, not a published one of the same name
      return <span className="md-wsfile md-wslink" data-workspace-path={ref.path} {...wsProps(() => open(ref.path, ref.line, folder ? { folder: true } : undefined))}>{children}</span>
    }
    return <span className="md-link-inert" title={relative && !doc?.readWorkspace ? 'Not available: this file has no folder to open it from' : undefined}>{children}</span>
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

// An image in a document that has not loaded (or may not): its alt text in a small frame.
function ImagePlaceholder({ alt, children }: { alt: string; children?: ReactNode }) {
  return (
    <span className="md-img-placeholder" data-testid="md-img-placeholder">
      <span className="md-img-alt" title={alt || undefined}>{alt || 'Image'}</span>
      {children}
    </span>
  )
}

type Dim = string | number | undefined

// A remote image: never fetched until the person asks, then loaded without a referrer. The host
// the click will contact is in the visible text, not only a tooltip (a phone has none).
function RemoteImage({ url, alt, width, height }: { url: string; alt: string; width: Dim; height: Dim }) {
  const [load, setLoad] = useState(false)
  const host = new URL(url).host
  if (load) return <img className="md-img" src={url} alt={alt} width={width} height={height} referrerPolicy="no-referrer" loading="lazy" decoding="async" />
  return (
    <ImagePlaceholder alt={alt}>
      <span className="md-img-host">{host}</span>
      <button
        type="button"
        className="md-img-load"
        title={`Load the image from ${host}`}
        onClick={e => { e.preventDefault(); e.stopPropagation(); setLoad(true) }}
      >Load image</button>
    </ImagePlaceholder>
  )
}

// A relative image in a folder file: read through the workspace-file route (the session's own
// authorization), drawn from a blob URL. Anything but an image file stays the placeholder; a read
// that really failed (not found, refused, too large) offers Retry.
function WorkspaceImage({ path, read, alt, width, height }: { path: string; read: NonNullable<MarkdownDocument['readWorkspace']>; alt: string; width: Dim; height: Dim }) {
  const [attempt, setAttempt] = useState(0)
  return <WorkspaceImageRead key={attempt} path={path} read={read} alt={alt} width={width} height={height} onRetry={() => setAttempt(n => n + 1)} />
}

function WorkspaceImageRead({ path, read, alt, width, height, onRetry }: { path: string; read: NonNullable<MarkdownDocument['readWorkspace']>; alt: string; width: Dim; height: Dim; onRetry: () => void }) {
  const [url, setUrl] = useState<string | null>(null)
  // 'retry': the read failed and reading again may help; 'final': the file is not an image.
  const [failed, setFailed] = useState<false | 'retry' | 'final'>(false)
  const { inLink, slot } = useContext(AfterLink)
  useEffect(() => {
    const ac = new AbortController()
    let made: string | null = null
    // Through the page's image queue: two reads at a time. While the server is only busy it keeps
    // trying, for as long as the document is open (the abort on unmount ends it).
    queuedRead(signal => read(path, signal), ac.signal, { untilAborted: true }).then(
      async file => {
        if (ac.signal.aborted) return
        if (file.view !== 'image' || !(imageBlobType(file.content_type) || isSvgImage(file.view, file.content_type))) { setFailed('final'); return }
        // A typed blob: URL, or for an SVG a data: URL (never a same-origin blob: one); only ever
        // drawn by an <img>, where an SVG's script does not run.
        const u = await mediaUrl(file.blob, file.view, file.content_type)
        if (ac.signal.aborted) { releaseMediaUrl(u); return }
        made = u
        setUrl(u)
      },
      () => { if (!ac.signal.aborted) setFailed('retry') },
    )
    return () => { ac.abort(); releaseMediaUrl(made) }
  }, [path, read])
  if (url) return <img className="md-img" src={url} alt={alt} width={width} height={height} data-workspace-path={path} decoding="async" />
  if (failed === 'final') return <ImagePlaceholder alt={alt} />
  if (failed === 'retry') {
    const retry = <button type="button" className="md-img-load" title={`Read ${path} again`} aria-label={`Retry ${alt || path}`} onClick={e => { e.preventDefault(); e.stopPropagation(); onRetry() }}>Retry</button>
    if (!inLink) return <ImagePlaceholder alt={alt}>{retry}</ImagePlaceholder>
    return <><ImagePlaceholder alt={alt} />{slot && createPortal(retry, slot)}</>
  }
  return <ImagePlaceholder alt={alt || 'Loading image'} />
}

function MarkdownImage({ src, alt, width, height, 'data-blerg-img': index }: { src?: string; alt?: string; width?: Dim; height?: Dim; 'data-blerg-img'?: number }) {
  const { doc, allImages, showAllImages } = useContext(Fence)
  const label = alt || 'image'
  const raw = typeof src === 'string' ? src : null
  if (!doc) {
    // Chat text: never loaded, a link to the image at most.
    const url = safeUrl(raw)
    if (!url) return <span className="md-link-inert">[{label}]</span>
    return (
      <a href={url} target="_blank" rel="noopener noreferrer" onClick={e => e.stopPropagation()}>
        [{label}]
      </a>
    )
  }
  const kind = classifyUrl(raw, 'src')
  const text = alt ?? ''
  if (kind?.kind === 'data-image') return <img className="md-img" src={kind.url} alt={text} width={width} height={height} />
  if (kind?.kind === 'remote') return <RemoteImage url={kind.url} alt={text} width={width} height={height} />
  if (kind?.kind === 'relative' && doc.readWorkspace) {
    const resolved = resolveDocPath(doc.path, kind.url)
    const path = resolved ? workspaceFromHref(resolved)?.path : null
    if (path && !allImages && (index ?? 0) >= MAX_DOC_IMAGES) {
      // Past the cap a document reads nothing by itself: one click reads the rest.
      return (
        <ImagePlaceholder alt={text}>
          <button type="button" className="md-img-load" onClick={e => { e.preventDefault(); e.stopPropagation(); showAllImages?.() }}>Load images</button>
        </ImagePlaceholder>
      )
    }
    if (path) return <WorkspaceImage key={path} path={path} read={doc.readWorkspace} alt={text} width={width} height={height} />
  }
  return <ImagePlaceholder alt={text} />
}

const components: Components = {
  a: MarkdownLink,
  // A reference candidate the refs step marked (only there: Fence.refs), else the span it is.
  span: function Span(props) {
    const { refs } = useContext(Fence)
    return <RefSpanFor enabled={!!refs} {...(props as Record<string, unknown>)} />
  },
  code: InlineCode,
  img: MarkdownImage,
  pre({ children, node }) {
    return <FencedBlock position={node?.position}>{children}</FencedBlock>
  },
  // `align` comes only from raw HTML, already limited to left/center/right/justify.
  table({ children, align }) {
    return (
      <div className={`md-table-wrap${align === 'center' ? ' is-centered' : ''}`}>
        <table align={align}>{children}</table>
      </div>
    )
  },
}

// Links are vetted in the `a` component above; the transform only has to
// keep react-markdown from rewriting them first.
const keepUrl = (url: string) => url

// Headings get their ids and images their order everywhere; raw HTML is sanitized into allowlisted
// elements only where the host opted in (`html`). Elsewhere react-markdown shows it as literal text.
type Plugins = NonNullable<Parameters<typeof ReactMarkdown>[0]['rehypePlugins']>
const textPlugins = [rehypeHeadingIds, rehypeImageIndex] as unknown as Plugins
const refPlugins = [rehypeHeadingIds, rehypeImageIndex, rehypeRefs] as unknown as Plugins
const htmlPlugins = [rehypeSafeHtml, rehypeHeadingIds, rehypeImageIndex] as unknown as Plugins

/** Where a document came from: what its relative links and images resolve against. */
export interface MarkdownDocument {
  /** The file's path in the session's folder (relative links and images resolve beside it). */
  path?: string
  /** Reads a file from the session's folder (the workspace-file route). Without it (a published
   *  file has no folder) relative images stay placeholders. */
  readWorkspace?: (path: string, signal?: AbortSignal) => Promise<WorkspaceFile>
}

export interface MarkdownProps {
  text: string
  className?: string
  /** The app's cards; without one every card fence is a code block. */
  cards?: CardRegistry
  /** Agent-authored text: relative links and path-like inline code open the file from the session's
   *  folder, when the host offers it (ChatView's canOpenWorkspaceFiles). Never for a person's text. */
  workspaceLinks?: boolean
  /** A document rather than chat text: images may load (see MarkdownDocument) and relative links
   *  resolve against the file's own path. */
  document?: MarkdownDocument
  /** Render raw HTML through GitHub's allowlist (model/markdownHtml.ts). Off by default: only a
   *  document surface opts in (the file viewer, the folder-file viewer, review mode). Everywhere
   *  else (chat, approvals, tickets, threads) HTML stays literal text, so nothing can be hidden from
   *  the person reading it. */
  html?: boolean
}

function MarkdownImpl({ text, className, cards, workspaceLinks, document: doc, html = false }: MarkdownProps) {
  const [allImages, setAllImages] = useState(false)
  // Chat text in a chat that lends references (never a document, never raw HTML).
  const refs = !!useRefContext() && !doc && !html
  return (
    <div className={`md${className ? ` ${className}` : ''}`}>
      <Fence.Provider value={{ cards, source: text, workspace: workspaceLinks, doc, allImages, showAllImages: () => setAllImages(true), refs }}>
        <ReactMarkdown remarkPlugins={[remarkGfm]} rehypePlugins={html ? htmlPlugins : refs ? refPlugins : textPlugins} components={components} urlTransform={keepUrl}>
          {text}
        </ReactMarkdown>
      </Fence.Provider>
    </div>
  )
}

/** Markdown renders `text` as safe, formatted Markdown, with the app's cards in place of card fences. */
const Markdown = memo(MarkdownImpl)
export default Markdown
