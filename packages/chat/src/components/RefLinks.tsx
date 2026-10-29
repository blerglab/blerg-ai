// References in chat text, drawn (card #153): a rehype step marks the candidates in a message's
// prose (never inside a fenced code block, never inside a link; an inline code span only when it
// holds nothing but a hash), and RefSpan draws each one as the text it is until the store confirms
// it, then as a link in the same place: a hovercard on hover, a click opening the ticket or the
// commit viewer. Model: model/refs.ts.
import './markdown.css'
import { createContext, useContext, useEffect, useMemo, useSyncExternalStore, type ReactNode } from 'react'
import { Hover } from './Hovercard'
import { useCommitOpener } from './CommitViewer'
import { isHash, splitRefs, refStoreFor, type ChatRefs, type RefKey, type RefStore } from '../model/refs'

export interface RefContextValue {
  store: RefStore
  sessionId: string
  refs?: ChatRefs
}

/** The chat's references: absent outside a chat that lends them (a document, another surface). */
export const RefContext = createContext<RefContextValue | null>(null)

export function useRefContext(): RefContextValue | null {
  return useContext(RefContext)
}

// ── The rehype step ─────────────────────────────────────────────────────────────────────────────
type HNode = { type: string; tagName?: string; value?: string; properties?: Record<string, unknown>; children?: HNode[] }

const ATTR = 'dataBlergRef'
const RANGE = 'dataBlergRange'

function textOf(n: HNode): string {
  if (n.type === 'text') return n.value ?? ''
  return (n.children ?? []).map(textOf).join('')
}

function mark(key: RefKey, children: HNode[]): HNode {
  return { type: 'element', tagName: 'span', properties: { [ATTR]: key }, children }
}

function walk(node: HNode) {
  const kids = node.children
  if (!kids) return
  const out: HNode[] = []
  for (const c of kids) {
    if (c.type === 'element') {
      // A fenced block, a link, a heading's anchor: left as they are.
      if (c.tagName === 'pre' || c.tagName === 'a') { out.push(c); continue }
      if (c.tagName === 'code') {
        // Inline code holding only a hash may link; any other code stays code.
        const t = textOf(c).trim()
        out.push(isHash(t) ? mark(`commit:${t.toLowerCase()}`, [c]) : c)
        continue
      }
      walk(c)
      out.push(c)
      continue
    }
    if (c.type === 'text' && c.value) {
      const pieces = splitRefs(c.value)
      if (pieces.length === 1 && !('key' in pieces[0]) && !('cards' in pieces[0])) { out.push(c); continue }
      for (const p of pieces) out.push('cards' in p
        ? { type: 'element', tagName: 'span', properties: { [RANGE]: p.cards.join(',') }, children: [{ type: 'text', value: p.text }] }
        : 'key' in p ? mark(p.key, [{ type: 'text', value: p.text }]) : { type: 'text', value: p.text })
      continue
    }
    out.push(c)
  }
  node.children = out
}

/** Marks reference candidates in the tree react-markdown renders. */
export function rehypeRefs() {
  return (tree: HNode) => { walk(tree) }
}

// ── The span ────────────────────────────────────────────────────────────────────────────────────
/** react-markdown's `span`: where the refs step ran (enabled), a marked candidate is a reference;
 *  any other span is itself. */
export function RefSpanFor(props: { enabled: boolean } & Record<string, unknown>) {
  const { enabled, children, ...rest } = props
  delete rest.node
  const range = rest['data-blerg-range']
  delete rest['data-blerg-range']
  if (enabled && typeof range === 'string') return <RangeSpan numbers={range.split(',').map(Number)}>{children as ReactNode}</RangeSpan>
  const key = rest['data-blerg-ref']
  if (enabled && typeof key === 'string' && (key.startsWith('card:') || key.startsWith('commit:'))) return <RefSpan refKey={key as RefKey}>{children as ReactNode}</RefSpan>
  delete rest['data-blerg-ref']
  return <span {...rest}>{children as ReactNode}</span>
}

const none = () => () => {}

function RangeSpan({ numbers, children }: { numbers: number[]; children?: ReactNode }) {
  const ctx = useRefContext()
  const store = ctx?.store
  const keys = numbers.map(n => `card:${n}` as RefKey)
  const signature = keys.join(',')
  const states = useSyncExternalStore(store ? store.subscribe : none,
    () => keys.map(k => store?.get(k).state ?? 'off').join(','), () => '')
  useEffect(() => { for (const k of signature.split(',')) store?.want(k as RefKey) }, [store, signature])
  if (!ctx?.refs?.openCard || states.split(',').some(s => s !== 'card')) return <span>{children}</span>
  return <>{keys.map((key, i) => <span key={key}>{i > 0 && ', '}<RefSpan refKey={key}>#{numbers[i]}</RefSpan></span>)}</>
}

function RefSpan({ refKey, children }: { refKey: RefKey; children?: ReactNode }) {
  const ctx = useRefContext()
  const opener = useCommitOpener()
  const store = ctx?.store
  const state = useSyncExternalStore(store ? store.subscribe : none, () => store?.get(refKey) ?? null, () => null)
  useEffect(() => { store?.want(refKey) }, [store, refKey])
  if (ctx && state?.state === 'card' && ctx.refs?.openCard) {
    const open = ctx.refs.openCard
    const target = state.target
    return (
      <Hover target={target}>
        {/* button-guard-ignore: a reference link (in Markdown, diff headers and review commit lists), styled by button.md-ref (markdown.css). */}
        <button type="button" className="md-ref md-ref-card" data-ref={refKey} data-testid="md-ref"
          aria-label={`Open card ${target.key}${target.title ? `: ${target.title}` : ''}`}
          onClick={e => { e.stopPropagation(); open(target) }}>{target.key}</button>
      </Hover>
    )
  }
  if (ctx && state?.state === 'commit' && opener) {
    const sha = state.sha
    return (
      <Hover target={{ kind: 'commit', sessionId: ctx.sessionId, sha }}>
        {/* button-guard-ignore: a reference link (in Markdown, diff headers and review commit lists), styled by button.md-ref (markdown.css). */}
        <button type="button" className="md-ref md-ref-commit" data-ref={refKey} data-testid="md-ref"
          aria-label={`Open commit ${sha.slice(0, 7)}`} onClick={e => { e.stopPropagation(); opener(ctx.sessionId, sha, undefined, ctx) }}>{children}</button>
      </Hover>
    )
  }
  // Not (yet) confirmed: the text exactly as it is.
  return <span data-ref-candidate={refKey} data-ref-state={state?.state ?? 'off'}>{children}</span>
}

/** The same confirmed card references in host surfaces such as History and plans. */
export function ReferenceText({ text }: { text: string }) {
  return <>{splitRefs(text).map((p, i) => 'key' in p
    ? <RefSpan key={i} refKey={p.key}>{p.text}</RefSpan>
    : 'cards' in p ? <RangeSpan key={i} numbers={p.cards}>{p.text}</RangeSpan> : p.text)}</>
}


/** Lends a host surface the same session-scoped resolver and hovercard behavior as chat. */
export function ReferenceScope({ sessionId, refs, children }: { sessionId: string; refs: ChatRefs; children: ReactNode }) {
  const store = refStoreFor(sessionId)
  useEffect(() => { store.setResolvers({ cards: refs.cards, boardCards: refs.boardCards, commit: null }) }, [store, refs.cards, refs.boardCards])
  const value = useMemo(() => ({ store, sessionId, refs }), [store, sessionId, refs])
  return <RefContext.Provider value={value}>{children}</RefContext.Provider>
}
