// Rich copy and paste: HTML to Markdown (for a paste into the message box and for a copy out of
// the chat) and the sanitised HTML a copy puts beside it.
//
// Hand-written on the DOM rather than a library: about 6 kB gzipped, nothing to audit but this
// file, and the output is Markdown text by construction, so raw HTML never survives. What is not
// listed here (script, style, iframe, form, image, comment, every attribute but a safe href and a
// code language) is dropped.
import { safeUrl } from './safeUrl'

const DROP = new Set([
  'script', 'style', 'iframe', 'frame', 'frameset', 'form', 'img', 'picture', 'svg', 'math', 'noscript',
  'object', 'embed', 'video', 'audio', 'canvas', 'template', 'head', 'title', 'meta', 'link', 'base',
  'button', 'input', 'select', 'textarea', 'option', 'dialog',
])
const BLOCK = new Set([
  'p', 'div', 'section', 'article', 'header', 'footer', 'main', 'aside', 'nav', 'figure', 'figcaption', 'details',
  'summary', 'address', 'fieldset', 'dl', 'dt', 'dd', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'li',
  'pre', 'blockquote', 'table', 'thead', 'tbody', 'tfoot', 'tr', 'td', 'th', 'caption', 'hr', 'center',
])

/** What a copy out of the chat leaves behind: the app's own chrome inside a message. */
export const COPY_CHROME = 'button, time, .card-foot, .turn-foot, .card-author, .message-kind, .md-codeblock-actions, .md-card-note, [data-nocopy]'

interface Ctx {
  /** Set when anything with Markdown meaning was emitted: false means the HTML was plain text in spans. */
  rich: boolean
  /** Inside a table cell or a heading: no line breaks. */
  flat: boolean
  /** In a heading, where bold is the heading's own look. */
  noBold?: boolean
  /** Elements that have a block element somewhere below them, worked out once. */
  blockBelow: WeakSet<Element>
}

const tag = (n: Node) => (n.nodeType === 1 ? (n as Element).tagName.toLowerCase() : '')
const isEl = (n: Node): n is HTMLElement => n.nodeType === 1
const style = (el: Element) => (el.getAttribute('style') ?? '').toLowerCase()

function boldOf(el: Element): boolean {
  const t = tag(el)
  const s = style(el)
  const w = /font-weight:\s*([a-z0-9]+)/.exec(s)?.[1]
  if (w) return w === 'bold' || w === 'bolder' || Number(w) >= 600
  return t === 'strong' || t === 'b'
}
const italicOf = (el: Element) => {
  const f = /font-style:\s*([a-z]+)/.exec(style(el))?.[1]
  return f ? f === 'italic' || f === 'oblique' : tag(el) === 'em' || tag(el) === 'i' || tag(el) === 'cite'
}
const strikeOf = (el: Element) => /text-decoration[a-z-]*:[^;]*line-through/.test(style(el)) || ['del', 's', 'strike'].includes(tag(el))

function escapeText(s: string): string {
  return s
    .replace(/\\(?=[!-/:-@[-`{-~])/g, '\\\\')
    .replace(/`/g, '\\`')
    .replace(/\*(?=\S)|(?<=\S)\*/g, '\\*')
    .replace(/(?<![\p{L}\p{N}])_|_(?![\p{L}\p{N}])/gu, '\\_')
    .replace(/~~/g, '\\~\\~')
    .replace(/\]\(/g, '\\](')
    .replace(/</g, '\\<')
    .replace(/&(?=#?\w+;)/g, '\\&')
}

function wrap(mark: string, inner: string, ctx: Ctx): string {
  const m = /^(\s*)([\s\S]*?)(\s*)$/.exec(inner)!
  if (!m[2]) return inner
  ctx.rich = true
  return `${m[1]}${mark}${m[2]}${mark}${m[3]}`
}

function codeSpan(text: string): string {
  const t = text.replace(/\s+/g, ' ')
  const runs: string[] = t.match(/`+/g) ?? []
  let n = 1
  while (runs.includes('`'.repeat(n))) n++
  const fence = '`'.repeat(n)
  return /^`|`$/.test(t) ? `${fence} ${t} ${fence}` : `${fence}${t}${fence}`
}

function inline(nodes: Iterable<Node>, ctx: Ctx): string {
  let out = ''
  for (const n of nodes) {
    if (n.nodeType === 3) {
      out += escapeText((n.nodeValue ?? '').replace(/[ \t\r\n\f\u00a0\u200b]+/g, ' '))
      continue
    }
    if (!isEl(n)) continue
    const t = tag(n)
    if (DROP.has(t)) continue
    if (t === 'br') { out += ctx.flat ? ' ' : '  \n'; continue }
    if (t === 'code' || t === 'kbd' || t === 'samp' || t === 'tt') {
      const text = n.textContent ?? ''
      if (text.trim()) { ctx.rich = true; out += codeSpan(text) }
      continue
    }
    if (t === 'a') {
      const href = safeUrl(n.getAttribute('href'))
      const text = inline(n.childNodes, ctx)
      if (!href || !text.trim()) { out += text; continue }
      ctx.rich = true
      const url = href.replace(/[()\s<>]/g, c => '%' + c.charCodeAt(0).toString(16).toUpperCase().padStart(2, '0'))
      out += text.trim() === href ? href : text.replace(/^(\s*)([\s\S]*?)(\s*)$/, `$1[$2](${url})$3`)
      continue
    }
    let inner = inline(n.childNodes, ctx)
    if (BLOCK.has(t)) { out += ` ${inner} `; continue }
    if (boldOf(n) && !ctx.noBold) inner = wrap('**', inner, ctx)
    if (italicOf(n)) inner = wrap('*', inner, ctx)
    if (strikeOf(n)) inner = wrap('~~', inner, ctx)
    out += inner
  }
  return out.replace(/\*\*\*\*(?!\*)|~~~~/g, '')
}

const LINE_START = /^(#{1,6}\s|>|[-+]\s)|^(\d+)([.)]\s)/
// Escapes what would start a block at the head of each line (after a line break too).
const para = (s: string) => s.replace(/[ \t]+\n/g, m => (m.startsWith('  ') ? '  \n' : '\n')).replace(/^[ \t]+|[ \t]+$/g, '').replace(/ {2,}(?!\n)/g, ' ')
  .split('\n').map(l => l.replace(LINE_START, (_m, a: string | undefined, n: string | undefined, d: string | undefined) => (a !== undefined ? `\\${a}` : `${n}\\${d}`))).join('\n')

function langOf(pre: Element): string {
  const code = pre.querySelector('code')
  const cands = [code?.className, pre.className, pre.parentElement?.className, pre.getAttribute('data-lang'), pre.getAttribute('data-language')]
  for (const c of cands) {
    const m = /(?:^|\s)(?:language|lang|highlight-source|brush:?)[-_ ]?([\w#+.-]+)/.exec(c ?? '')
    if (m) return m[1]
    if (c && /^[\w#+.-]+$/.test(c) && c === pre.getAttribute('data-lang')) return c
  }
  return ''
}

function preText(el: Node): string {
  let s = ''
  for (const n of Array.from(el.childNodes)) {
    if (n.nodeType === 3) s += n.nodeValue ?? ''
    else if (tag(n) === 'br') s += '\n'
    else if (isEl(n) && !DROP.has(tag(n))) {
      s += preText(n)
      if (tag(n) === 'div' && !s.endsWith('\n')) s += '\n'
    }
  }
  return s
}

export function fenced(text: string, lang: string): string {
  const body = text.replace(/\n+$/, '')
  const runs: string[] = body.match(/`{3,}/g) ?? []
  let n = 3
  while (runs.some(r => r.length >= n)) n++
  const f = '`'.repeat(n)
  return `${f}${lang}\n${body}\n${f}`
}

function cellText(cell: Element, ctx: Ctx): string {
  return inline(cell.childNodes, { ...ctx, flat: true }).replace(/\s+/g, ' ').trim().replace(/\|/g, '\\|')
}

function table(el: Element, ctx: Ctx): string[] {
  const rows = Array.from(el.querySelectorAll('tr')).filter(r => r.closest('table') === el)
    .map(r => Array.from(r.children).filter(c => /^(td|th)$/i.test(c.tagName)))
    .filter(r => r.length > 0)
  if (rows.length === 0) return []
  if (rows.length === 1 && rows[0].length === 1) return blocks(rows[0][0].childNodes, ctx)
  const width = Math.max(...rows.map(r => r.length))
  const line = (r: Element[]) => `| ${Array.from({ length: width }, (_, i) => (r[i] ? cellText(r[i], ctx) : '')).join(' | ')} |`
  ctx.rich = true
  const [head, ...rest] = rows
  return [[line(head), `| ${Array.from({ length: width }, () => '---').join(' | ')} |`, ...rest.map(line)].join('\n')]
}

function list(el: Element, ctx: Ctx): string {
  const ordered = tag(el) === 'ol'
  let n = Number.parseInt(el.getAttribute('start') ?? '1', 10)
  if (!Number.isFinite(n)) n = 1
  const items: string[] = []
  for (const c of Array.from(el.children)) {
    const t = tag(c)
    if ((t === 'ul' || t === 'ol') && items.length > 0) {
      // Docs puts a nested list beside the item rather than in it.
      items[items.length - 1] += '\n' + indent(list(c, ctx), 2)
      continue
    }
    if (t !== 'li') continue
    const task = c.querySelector(':scope > input[type=checkbox], :scope > p > input[type=checkbox], :scope > label > input[type=checkbox]')
    const parts = blocks(c.childNodes, ctx)
    let first = parts.shift() ?? ''
    if (task) first = `[${(task as HTMLInputElement).hasAttribute('checked') ? 'x' : ' '}] ${first}`
    const marker = ordered ? `${n++}. ` : '- '
    const tight = parts.every(p => /^(?: {0,3}[-*+] |\d+[.)] )/.test(p) || p.startsWith('- '))
    const body = [first, ...parts].join(tight ? '\n' : '\n\n')
    items.push(marker + indent(body, marker.length).slice(marker.length))
  }
  ctx.rich = ctx.rich || items.length > 0
  return items.join('\n')
}

const indent = (s: string, n: number) => s.split('\n').map(l => (l ? ' '.repeat(n) + l : l)).join('\n')

function block(el: HTMLElement, ctx: Ctx): string[] {
  const t = tag(el)
  if (DROP.has(t)) return []
  if (/^h[1-6]$/.test(t)) {
    const text = inline(el.childNodes, { ...ctx, flat: true, noBold: true }).replace(/\s+/g, ' ').trim()
    if (!text) return []
    ctx.rich = true
    return [`${'#'.repeat(Number(t[1]))} ${text}`]
  }
  if (t === 'ul' || t === 'ol') { const l = list(el, ctx); return l ? [l] : [] }
  if (t === 'pre') {
    const text = preText(el)
    if (!text.trim()) return []
    ctx.rich = true
    return [fenced(text, langOf(el))]
  }
  if (t === 'blockquote') {
    const inner = blocks(el.childNodes, ctx).join('\n\n')
    if (!inner.trim()) return []
    ctx.rich = true
    return [inner.split('\n').map(l => (l ? `> ${l}` : '>')).join('\n')]
  }
  if (t === 'hr') { ctx.rich = true; return ['---'] }
  if (t === 'table') return table(el, ctx)
  return blocks(el.childNodes, ctx)
}

function blocks(nodes: Iterable<Node>, ctx: Ctx): string[] {
  const out: string[] = []
  let run: Node[] = []
  const flush = () => {
    if (run.length === 0) return
    const text = para(inline(run, ctx))
    run = []
    if (text.trim()) out.push(text)
  }
  for (const n of nodes) {
    if (isEl(n) && (BLOCK.has(tag(n)) || (tag(n) !== 'a' && !DROP.has(tag(n)) && ctx.blockBelow.has(n)))) { flush(); out.push(...block(n, ctx)) } else run.push(n)
  }
  flush()
  return out
}

export interface Converted { markdown: string; rich: boolean }

/** A DOM subtree as Markdown. `rich` says whether it carried any structure beyond plain text. */
export function nodeToMarkdown(root: Node): Converted {
  const ctx: Ctx = { rich: false, flat: false, blockBelow: blockBelowOf(root) }
  const markdown = blocks(root.childNodes, ctx).join('\n\n').replace(/\n{3,}/g, '\n\n').trim()
  return { markdown, rich: ctx.rich }
}

/** One pass, children before parents: which elements have a block element below them. */
function blockBelowOf(root: Node): WeakSet<Element> {
  const all: Element[] = []
  const stack: Node[] = [root]
  for (let n = stack.pop(); n; n = stack.pop()) {
    if (isEl(n)) all.push(n)
    for (let c = n.lastChild; c; c = c.previousSibling) stack.push(c)
  }
  const set = new WeakSet<Element>()
  for (let i = all.length - 1; i >= 0; i--) {
    const el = all[i]
    const parent = el.parentElement
    if (parent && (BLOCK.has(tag(el)) || set.has(el))) set.add(parent)
  }
  return set
}

/** What a paste may cost: past these the clipboard's plain text goes in as it is. */
export const PASTE_MAX_HTML = 512 * 1024
export const PASTE_MAX_NODES = 20_000
export const PASTE_MAX_DEPTH = 64

/** A linear look at the text before it is parsed (a parser can be slow on what a page nests):
 *  too many tags, or containers opened past the depth limit. */
function textWithinBudget(html: string): boolean {
  let tags = 0
  let depth = 0
  for (const m of html.matchAll(/<(\/?)([a-z][a-z0-9]*)/gi)) {
    if (++tags > PASTE_MAX_NODES) return false
    if (!NESTING.has(m[2].toLowerCase())) continue
    depth = m[1] ? Math.max(0, depth - 1) : depth + 1
    if (depth > PASTE_MAX_DEPTH) return false
  }
  return true
}
const NESTING = new Set(['ul', 'ol', 'blockquote', 'div', 'span', 'table', 'b', 'strong', 'i', 'em', 'a', 'section', 'details', 'font'])

function withinBudget(root: Node): boolean {
  let nodes = 0
  const stack: [Node, number][] = [[root, 0]]
  for (let item = stack.pop(); item; item = stack.pop()) {
    const [n, depth] = item
    if (++nodes > PASTE_MAX_NODES || depth > PASTE_MAX_DEPTH) return false
    for (let c = n.firstChild; c; c = c.nextSibling) stack.push([c, depth + 1])
  }
  return true
}

export function htmlToMarkdown(html: string): Converted {
  const doc = new DOMParser().parseFromString(html, 'text/html')
  return nodeToMarkdown(doc.body)
}

// ── Paste ────────────────────────────────────────────────────────────────────

const MD_SIGNALS = [/^ {0,3}(```|~~~)/m, /^\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$/m, /\[[^\]\n]+\]\(https?:[^)\s]+\)/, /\*\*[^*\n]+\*\*/, /^#{1,6} \S/m]

/** Does the text already read as Markdown source (so the HTML beside it adds nothing)? */
export function looksLikeMarkdown(text: string): boolean {
  const hits = MD_SIGNALS.filter(r => r.test(text)).length
  return hits >= 2 || MD_SIGNALS.slice(0, 4).some(r => r.test(text))
}

/** The Markdown to insert for a paste, or null to let the plain text go in as it is. */
export function markdownForPaste(html: string, plain: string): string | null {
  if (!html.trim() || html.length > PASTE_MAX_HTML) return null
  if (looksLikeMarkdown(plain)) return null
  try {
    if (!textWithinBudget(html)) return null
    const doc = new DOMParser().parseFromString(html, 'text/html')
    if (!withinBudget(doc.body)) return null
    const { markdown, rich } = nodeToMarkdown(doc.body)
    if (!rich || !markdown.trim()) return null
    return markdown
  } catch {
    return null
  }
}

// ── Copy ─────────────────────────────────────────────────────────────────────

const OUT_TAGS = new Set(['p', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'ul', 'ol', 'li', 'blockquote', 'hr', 'table', 'thead', 'tbody', 'tr', 'th', 'td', 'del', 'br'])
const esc = (s: string) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')

function html(nodes: Iterable<Node>): string {
  let out = ''
  for (const n of nodes) {
    if (n.nodeType === 3) { out += esc(n.nodeValue ?? ''); continue }
    if (!isEl(n)) continue
    const t = tag(n)
    if (DROP.has(t) && t !== 'input') continue
    if (t === 'input') {
      if ((n as HTMLInputElement).type === 'checkbox') out += `<input type="checkbox" disabled${n.hasAttribute('checked') || (n as HTMLInputElement).checked ? ' checked' : ''}>`
      continue
    }
    if (t === 'pre') { out += `<pre><code${langOf(n) ? ` class="language-${esc(langOf(n))}"` : ''}>${esc(preText(n).replace(/\n$/, ''))}</code></pre>`; continue }
    const inner = html(n.childNodes)
    if (t === 'a') {
      const href = safeUrl(n.getAttribute('href'))
      out += href ? `<a href="${esc(href)}">${inner}</a>` : inner
    } else if (t === 'code' || t === 'kbd' || t === 'samp' || t === 'tt') out += `<code>${inner}</code>`
    else if (OUT_TAGS.has(t)) out += t === 'hr' || t === 'br' ? `<${t}>` : `<${t}>${inner}</${t}>`
    else {
      let s = inner
      if (boldOf(n)) s = `<strong>${s}</strong>`
      if (italicOf(n)) s = `<em>${s}</em>`
      if (strikeOf(n) && t !== 'del') s = `<del>${s}</del>`
      out += s
    }
  }
  return out
}

/** The HTML for the clipboard: the formatting tags above, a safe href, nothing else. */
export function sanitizeToHtml(root: Node): string {
  return html(root.childNodes)
}

/** A copy of the chat's rendered markdown (or a selection of it) as both clipboard flavours. */
export function copyPayload(root: Node): { markdown: string; html: string } {
  const clone = root.cloneNode(true) as Element
  if (isEl(clone)) clone.querySelectorAll(COPY_CHROME).forEach(e => e.remove())
  return { markdown: nodeToMarkdown(clone).markdown, html: sanitizeToHtml(clone) }
}

/** A selection inside one code block copies its text exactly. */
export function codePayload(text: string, lang = ''): { markdown: string; html: string } {
  return { markdown: text, html: `<pre><code${lang ? ` class="language-${esc(lang)}"` : ''}>${esc(text)}</code></pre>` }
}
