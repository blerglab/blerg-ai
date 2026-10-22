// Raw HTML in Markdown, rendered the way GitHub does it: a small allowlist of elements and
// attributes, everything else dropped. READMEs center a banner with <p align="center">, put badges in
// links and lay screenshots out in <table>s; without this those blocks read as literal tags.
// DOCUMENTS only (Markdown's `html` prop: the file viewer, the folder-file viewer, review mode):
// every other surface (chat, approvals, tickets, threads) keeps HTML as literal text, so nothing a
// sanitizer drops (a comment, a closed <details>) can hide words from the person reading it.
//
// How (a rehype plugin, run before react-markdown turns the tree into React elements):
//   1. For every parent whose children include raw HTML, the children are written out as one HTML
//      string: raw HTML as it is, text escaped, and every node Markdown already made (a paragraph, a
//      code block, a link) as an empty placeholder element carrying its index. So a
//      `<details>` block that wraps Markdown paragraphs, or `<b>` around **bold** inside a paragraph,
//      parse as one piece.
//   2. DOMPurify parses that string with the browser's HTML parser and drops every element and
//      attribute that is not on the allowlist (with the content of script, style and the like).
//   3. The sanitized DOM is converted back into tree nodes, element by element, with the allowlist
//      checked again and every URL classified (below). Placeholders are swapped back for the nodes
//      they stood for, once each; the author cannot write one (the tag name is taken out of raw HTML
//      first). The result is cached per HTML string, so a streamed message is not sanitized again on
//      every delta. Past 200 levels of nesting a note says the rest was cut. Nothing is ever
//      serialized back into HTML and parsed again, so mutation XSS (markup that changes meaning on a
//      second parse) has no second parse to exploit, and React creates every element.
//
// URLs: a link may be http(s), relative or a #fragment; an image source http(s), relative or a
// base64 raster data URL. Anything else (javascript:, data:text/html, vbscript:, a protocol-relative
// `//host` link with a backslash trick) loses the attribute. What a kept URL then does (open in
// Blerg, a new tab, scroll; load an image only on a click) is decided by Markdown's components.
//
// Headings get GitHub's ids, prefixed `user-content-` so a document can never take an id the app
// uses; raw HTML cannot set id or name at all.
import createDOMPurify, { type DOMPurify } from 'dompurify'

/** Elements raw HTML may use. Anything else is dropped (script, style, iframe, form, svg, …). */
export const HTML_TAGS: readonly string[] = [
  'p', 'div', 'span', 'img', 'a', 'table', 'thead', 'tbody', 'tr', 'td', 'th', 'b', 'i', 'em', 'strong',
  'br', 'hr', 'sub', 'sup', 'details', 'summary', 'picture', 'source', 'kbd', 'code', 'pre', 'ul', 'ol',
  'li', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'blockquote',
]
/** Attributes raw HTML may use. Event handlers, style, id, name, class, srcdoc, … are dropped. */
export const HTML_ATTRS: readonly string[] = [
  'align', 'width', 'height', 'alt', 'title', 'href', 'src', 'srcset', 'colspan', 'rowspan', 'open',
]

const TAGS = new Set(HTML_TAGS)
// The placeholder's tag name carries a random suffix per page load, on top of taking the base name
// out of raw HTML (sanitizeChildren): an author would have to guess it as well.
function randomSuffix(): string {
  const a = new Uint32Array(2)
  if (typeof crypto !== 'undefined' && crypto.getRandomValues) crypto.getRandomValues(a)
  else { a[0] = Math.floor(Math.random() * 2 ** 32); a[1] = Math.floor(Math.random() * 2 ** 32) }
  return `${a[0].toString(36)}${a[1].toString(36)}`
}
const SLOT = `blerg-md-slot-${randomSuffix()}`
const SLOT_ATTR = 'data-blerg-slot'
/** The placeholder's tag name on this page (tests). */
export function placeholderTagForTests(): string { return SLOT }

// The tree nodes this works on (hast, kept minimal so the package does not lean on its types).
export interface HNode {
  type: string
  tagName?: string
  properties?: Record<string, unknown>
  children?: HNode[]
  value?: string
}

export type UrlKind = 'anchor' | 'relative' | 'remote' | 'data-image'

const BASE = 'https://base.invalid/dir/'

export interface DocUrl { kind: UrlKind; url: string }

/** What a URL in a document is (and the URL as a browser reads it), or null when it may not be used.
 *  `href` allows http(s), relative and #fragments; `src` allows http(s), relative and base64
 *  png/jpeg/gif/webp data. */
export function classifyUrl(raw: string | null | undefined, use: 'href' | 'src'): DocUrl | null {
  if (raw == null) return null
  // Browsers ignore leading/trailing C0 controls and spaces, and tabs and newlines anywhere.
  // eslint-disable-next-line no-control-regex
  const v = raw.replace(/^[\u0000- ]+|[\u0000- ]+$/g, '').replace(/[\t\n\r]/g, '')
  if (v === '' || v.length > 8192) return null
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(v)) return null
  if (use === 'src' && /^data:image\/(?:png|jpeg|jpg|gif|webp);base64,[a-z0-9+/= ]+$/i.test(v)) return { kind: 'data-image', url: v }
  if (v.startsWith('#')) return use === 'href' ? { kind: 'anchor', url: v } : null
  let u: URL
  try {
    u = new URL(v, BASE)
  } catch {
    return null
  }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return null
  // Relative only when the text has no scheme and does not reach another host (`//x`, `\\x`, `/\x`).
  const hasScheme = /^[a-z][a-z0-9+.-]*:/i.test(v)
  if (!hasScheme && u.origin === new URL(BASE).origin && !/^[/\\]{2}/.test(v) && !v.includes('\\')) return { kind: 'relative', url: v }
  if (!hasScheme) return null // protocol-relative or backslash tricks: refused rather than guessed
  // An absolute URL stands on its own: `https:foo` (no `//`) would borrow the dummy base's host.
  let abs: URL
  try { abs = new URL(v) } catch { return null }
  if (!/^https?:\/\//i.test(v) || abs.hostname === '' || abs.hostname === new URL(BASE).hostname) return null
  return { kind: 'remote', url: abs.href }
}

let purifier: DOMPurify | null = null
function purify(): DOMPurify | null {
  if (purifier) return purifier
  if (typeof window === 'undefined' || !window.document) return null
  // Its own instance: no hooks or config shared with any other user of DOMPurify on the page.
  purifier = createDOMPurify(window)
  return purifier
}

function escapeText(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

/** The largest width or height an image or cell keeps, in px; a percentage at most 100. */
export const MAX_DIMENSION = 4096

const PROP: Record<string, string> = {
  align: 'align', width: 'width', height: 'height', alt: 'alt', title: 'title', href: 'href', src: 'src',
  colspan: 'colSpan', rowspan: 'rowSpan', open: 'open',
}

function propsOf(el: Element): Record<string, unknown> {
  const tag = el.localName
  const out: Record<string, unknown> = {}
  for (const attr of Array.from(el.attributes)) {
    const name = attr.name.toLowerCase()
    const prop = PROP[name]
    if (!prop) continue // srcset included: an image loads one source, chosen by us
    const value = attr.value
    if (name === 'href') {
      if (tag !== 'a') continue
      const url = classifyUrl(value, 'href')
      if (url) out.href = url.url
    } else if (name === 'src') {
      if (tag !== 'img') continue
      const url = classifyUrl(value, 'src')
      if (url) out.src = url.url
    } else if (name === 'open') {
      if (tag === 'details') out.open = true
    } else if (name === 'colspan' || name === 'rowspan') {
      const n = Number.parseInt(value, 10)
      if ((tag === 'td' || tag === 'th') && Number.isFinite(n) && n >= 1 && n <= 1000) out[prop] = n
    } else if (name === 'width' || name === 'height') {
      const m = /^(\d{1,7})(?:\.\d+)?(%?)$/.exec(value.trim())
      if (m) out[prop] = m[2] ? `${Math.min(Number(m[1]), 100)}%` : String(Math.min(Number(m[1]), MAX_DIMENSION))
    } else if (name === 'align') {
      const a = value.trim().toLowerCase()
      if (['left', 'center', 'right', 'justify'].includes(a)) out.align = a
    } else {
      out[prop] = value.slice(0, 2000)
    }
  }
  return out
}

/** Nested deeper than this, raw HTML stops and says so (it is not dropped in silence). */
export const MAX_DEPTH = 200
export const TOO_DEEP = '…content too deeply nested'

// The sanitized form of one HTML string, kept so a streaming message does not sanitize the same
// block again on every delta. `slot` nodes stand for the Markdown nodes, by index.
interface Template { nodes: HNode[]; hidden: boolean }

function convert(node: Node, depth: number): HNode[] {
  if (depth > MAX_DEPTH) return [{ type: 'element', tagName: 'span', properties: { className: ['md-too-deep'] }, children: [{ type: 'text', value: TOO_DEEP }] }]
  if (node.nodeType === 3) return [{ type: 'text', value: (node as Text).data }]
  if (node.nodeType !== 1) return [] // comments, processing instructions
  const el = node as Element
  const tag = el.localName
  if (tag === SLOT) {
    const i = Number(el.getAttribute(SLOT_ATTR))
    return Number.isInteger(i) && i >= 0 ? [{ type: 'slot', value: String(i) }] : []
  }
  if (!TAGS.has(tag) || el.namespaceURI !== 'http://www.w3.org/1999/xhtml') return []
  // <source> only offers alternative images to load, which a document never does by itself.
  if (tag === 'source') return []
  const children: HNode[] = []
  for (const child of Array.from(el.childNodes)) {
    if (depth + 1 > MAX_DEPTH) { children.push(...convert(child, depth + 1)); break } // one note, not one per sibling
    children.push(...convert(child, depth + 1))
  }
  return [{ type: 'element', tagName: tag, properties: propsOf(el), children }]
}

let sanitizeRuns = 0
/** How many HTML strings were sanitized (not served from the cache): tests. */
export function htmlSanitizeRuns(): number { return sanitizeRuns }

function sanitizeHtml(dp: DOMPurify, html: string): Template {
  sanitizeRuns++
  const fragment = dp.sanitize(html, {
    ALLOWED_TAGS: [...HTML_TAGS, SLOT],
    ALLOWED_ATTR: [...HTML_ATTRS, SLOT_ATTR],
    ALLOW_DATA_ATTR: false,
    ALLOW_ARIA_ATTR: false,
    ALLOW_UNKNOWN_PROTOCOLS: false,
    SAFE_FOR_TEMPLATES: false,
    WHOLE_DOCUMENT: false,
    RETURN_DOM_FRAGMENT: true,
  })
  const out: HNode[] = []
  for (const node of Array.from(fragment.childNodes)) out.push(...convert(node, 0))
  return { nodes: out, hidden: droppedText(html, fragment) }
}

function visibleText(s: string | null | undefined): string {
  return (s ?? '').replace(/\s+/g, '')
}

// Whether sanitizing took away words the author wrote: a comment, or the text of an element dropped
// with its content (script, style, title, an unclosed <math>, …). Review mode says so ("This document
// has hidden content"). The input is parsed into an inert document (DOMParser runs no script and
// loads nothing) only to count its text.
function droppedText(html: string, fragment: DocumentFragment): boolean {
  if (typeof DOMParser === 'undefined') return false
  const doc = new DOMParser().parseFromString(`<!doctype html><body>${html}`, 'text/html')
  const walker = doc.createTreeWalker(doc.body, NodeFilter.SHOW_COMMENT)
  for (let c = walker.nextNode(); c; c = walker.nextNode()) if (visibleText((c as Comment).data) !== '') return true
  for (const el of Array.from(doc.querySelectorAll('template'))) if (visibleText((el as HTMLTemplateElement).content.textContent) !== '') return true
  return visibleText(fragment.textContent).length < visibleText(doc.body.textContent).length
}

const CACHE_MAX = 256
const cache = new Map<string, Template>()

function cached(dp: DOMPurify, html: string): Template {
  const hit = cache.get(html)
  if (hit) {
    cache.delete(html)
    cache.set(html, hit) // most recently used last
    return hit
  }
  const made = sanitizeHtml(dp, html)
  if (html.length <= 200_000) {
    cache.set(html, made)
    if (cache.size > CACHE_MAX) cache.delete(cache.keys().next().value as string)
  }
  return made
}

// A fresh copy of a template (later plugins change nodes in place), with each slot filled once by
// the Markdown-made element it stands for (never a raw or text child).
function instantiate(nodes: HNode[], children: HNode[], used: Set<number>): HNode[] {
  const out: HNode[] = []
  for (const n of nodes) {
    if (n.type === 'slot') {
      const i = Number(n.value)
      if (!used.has(i) && children[i]?.type === 'element') { used.add(i); out.push(children[i]) }
      continue
    }
    if (n.type === 'text') { out.push({ type: 'text', value: n.value }); continue }
    out.push({
      type: n.type,
      tagName: n.tagName,
      properties: { ...(n.properties ?? {}) },
      children: n.children ? instantiate(n.children, children, used) : [],
    })
  }
  return out
}

/** The sanitized nodes for one run of children that mixes raw HTML with Markdown. Exported for tests. */
export function sanitizeChildren(children: HNode[]): HNode[] {
  const dp = purify()
  if (!dp) return children.map(c => (c.type === 'raw' ? { type: 'text', value: c.value ?? '' } : c))
  let html = ''
  children.forEach((child, i) => {
    // An author cannot write a placeholder: tag names are literal in HTML (no entities), so taking
    // the name out of raw HTML leaves an unknown element that the sanitizer drops.
    if (child.type === 'raw') html += (child.value ?? '').replace(/blerg-md-slot/gi, 'x-blocked')
    else if (child.type === 'text') html += escapeText(child.value ?? '')
    else if (child.type === 'element') html += `<${SLOT} ${SLOT_ATTR}="${i}"></${SLOT}>`
  })
  const template = cached(dp, html)
  const used = new Set<number>()
  const out = instantiate(template.nodes, children, used)
  // Markdown that an unclosed element swallowed (<math>, <svg>, <title>, <xmp>, <iframe>, …: the
  // sanitizer drops them with everything inside) is not lost: it follows, behind a note.
  const unplaced = children.filter((c, i) => c.type === 'element' && !used.has(i))
  if (unplaced.length > 0) {
    out.push({ type: 'element', tagName: 'p', properties: { className: ['md-unclosed'] }, children: [{ type: 'text', value: UNCLOSED }] })
    out.push(...unplaced)
  }
  // An invisible marker where sanitizing took words away: review mode looks for it.
  if (template.hidden) out.push({ type: 'element', tagName: 'span', properties: { hidden: true, dataMdHidden: '' }, children: [] })
  return out
}

export const UNCLOSED = '…HTML above was not closed; the rest follows.'

function hasRaw(children: HNode[] | undefined): boolean {
  return !!children && children.some(c => c.type === 'raw')
}

function walk(node: HNode): void {
  if (!node.children) return
  // Inner parents first, so a placeholder carries its own already-sanitized subtree.
  for (const child of node.children) walk(child)
  if (hasRaw(node.children)) node.children = sanitizeChildren(node.children)
}

/** rehype plugin: raw HTML becomes allowlisted elements (see the top of this file). */
export function rehypeSafeHtml() {
  return (tree: HNode) => { walk(tree) }
}

// GitHub's heading slugs: lower case, punctuation dropped, spaces to dashes, repeats numbered.
function textOf(node: HNode): string {
  if (node.type === 'text') return node.value ?? ''
  return (node.children ?? []).map(textOf).join('')
}

export function slugify(text: string): string {
  return text.trim().toLowerCase().replace(/[^\p{L}\p{N}\p{M}\s_-]/gu, '').replace(/\s/g, '-')
}

export const HEADING_ID_PREFIX = 'user-content-'

/** rehype plugin: every heading gets `user-content-<slug>`, so `#slug` links can find it. */
export function rehypeHeadingIds() {
  return (tree: HNode) => {
    const seen = new Map<string, number>()
    const visit = (node: HNode) => {
      if (node.type === 'element' && /^h[1-6]$/.test(node.tagName ?? '')) {
        const base = slugify(textOf(node))
        const n = seen.get(base) ?? 0
        seen.set(base, n + 1)
        node.properties = { ...(node.properties ?? {}), id: HEADING_ID_PREFIX + (n ? `${base}-${n}` : base) }
      }
      for (const child of node.children ?? []) visit(child)
    }
    visit(tree)
  }
}

/** The folder path a relative URL in a document at `docPath` names, or null when it climbs out of the
 *  folder. The ?query and #fragment are kept for workspaceFromHref (it reads `#L12`). */
export function resolveDocPath(docPath: string | undefined, href: string): string | null {
  const m = /^([^?#]*)(.*)$/s.exec(href.trim())
  if (!m) return null
  const [, pathPart, suffix] = m
  if (pathPart === '') return null
  let decoded: string
  try { decoded = decodeURIComponent(pathPart) } catch { return null }
  const dir = docPath && docPath.includes('/') ? docPath.slice(0, docPath.lastIndexOf('/')).split('/') : []
  const parts = decoded.startsWith('/') ? [] : [...dir]
  for (const seg of decoded.split('/')) {
    if (seg === '' || seg === '.') continue
    if (seg === '..') {
      if (parts.length === 0) return null
      parts.pop()
    } else parts.push(seg)
  }
  if (parts.length === 0) return null
  return parts.map(encodeURIComponent).join('/') + suffix
}

/** rehype plugin: numbers the images in document order (`data-blerg-img`), so a document can load
 *  the first MAX_DOC_IMAGES and hold the rest. Raw HTML cannot set data attributes. */
export function rehypeImageIndex() {
  return (tree: HNode) => {
    let n = 0
    const visit = (node: HNode) => {
      if (node.type === 'element' && node.tagName === 'img') node.properties = { ...(node.properties ?? {}), dataBlergImg: n++ }
      for (const child of node.children ?? []) visit(child)
    }
    visit(tree)
  }
}

/** Images a document loads by itself; the rest wait for "Load images". */
export const MAX_DOC_IMAGES = 50

const IMAGE_TYPES = /^image\/(?:png|jpeg|gif|webp|avif|bmp|x-icon|vnd\.microsoft\.icon)$/

/** The content type a folder image's blob may carry, or '' for anything else. Never
 *  image/svg+xml: a same-origin blob: URL of an SVG opened in a tab runs as the app; an SVG is drawn
 *  from a data: URL (artifactViews' mediaUrl). */
export function imageBlobType(contentType: string): string {
  const t = contentType.split(';')[0].trim().toLowerCase()
  return IMAGE_TYPES.test(t) ? t : ''
}
