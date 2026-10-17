// Highlighting for the chat's code: lazy, cached, and never blocking. The first call starts
// loading the highlighter chunk (model/highlighter.ts); until it arrives, and for any text it
// cannot or should not colour, `highlightSync` answers null and the caller shows plain text.
import { useEffect, useReducer } from 'react'
import type { CodeLang } from './codeLang'

/** A run of text, or a classed span of runs: what the highlighter hands back. */
export type Token = { text: string } | { cls: string; kids: Token[] }

/** Text over this many characters is shown plain: colouring it would cost more than it is worth. */
export const HIGHLIGHT_MAX_CHARS = 100_000

type Highlighter = (lang: CodeLang, text: string) => Token[]

let ready: Highlighter | null = null
let loading: Promise<void> | null = null
/** Replaced in tests, which cannot code-split. */
let importer: () => Promise<{ highlightTokens: Highlighter }> = () => import('./highlighter')

/** Starts loading the highlighter (once). Resolves when it is ready; a failure leaves text plain. */
export function loadHighlighter(): Promise<void> {
  loading ??= importer().then(m => { ready = m.highlightTokens }, () => { loading = null })
  return loading
}

// Cache by (lang, text): a streaming message re-renders its closed fences on every delta, and
// each must cost a lookup, not a tokenizer run. Bounded, oldest out first.
const CACHE_MAX = 200
const cache = new Map<string, Token[]>()

/** The tokens of `text` in `lang`, or null when the highlighter is not loaded yet or the text
 *  is too big. Fully synchronous: it never starts a load (see useHighlight). */
export function highlightSync(lang: CodeLang, text: string): Token[] | null {
  if (!ready || text.length > HIGHLIGHT_MAX_CHARS) return null
  const key = `${lang}\u0000${text}`
  const hit = cache.get(key)
  if (hit) return hit
  let tokens: Token[]
  try {
    tokens = ready(lang, text)
  } catch {
    return null
  }
  if (cache.size >= CACHE_MAX) cache.delete(cache.keys().next().value as string)
  cache.set(key, tokens)
  return tokens
}

/** The tokens of `text`, once the highlighter is loaded; null (plain text) before, when `enabled`
 *  is false, or when the language is unknown. A streaming fence passes enabled=false. */
export function useHighlight(lang: CodeLang | null, text: string, enabled: boolean): Token[] | null {
  const [, bump] = useReducer((n: number) => n + 1, 0)
  const wanted = enabled && lang !== null && lang !== 'diff' && text.length <= HIGHLIGHT_MAX_CHARS
  useEffect(() => {
    if (!wanted || ready) return
    let live = true
    void loadHighlighter().then(() => { if (live) bump() })
    return () => { live = false }
  }, [wanted])
  return wanted ? highlightSync(lang, text) : null
}

/** Whether the highlighter is loaded; starts loading it when `enabled` (a view that colours many
 *  pieces itself, with highlightSync, re-renders once it arrives). */
export function useHighlighterReady(enabled: boolean): boolean {
  const [, bump] = useReducer((n: number) => n + 1, 0)
  useEffect(() => {
    if (!enabled || ready) return
    let live = true
    void loadHighlighter().then(() => { if (live) bump() })
    return () => { live = false }
  }, [enabled])
  return enabled && ready !== null
}

/** Tokens cut into lines at each newline: line i of the text is element i, every span that
 *  crosses a newline repeated on both sides. */
export function splitTokenLines(list: Token[]): Token[][] {
  const lines: Token[][] = [[]]
  const walk = (nodes: Token[], wrap: (t: Token) => Token) => {
    for (const n of nodes) {
      if ('text' in n) {
        const parts = n.text.split('\n')
        parts.forEach((part, i) => {
          if (i > 0) lines.push([])
          if (part) lines[lines.length - 1].push(wrap({ text: part }))
        })
      } else {
        // Each line's piece of this span is its own span with the same class.
        const cls = n.cls
        walk(n.kids, t => wrap({ cls, kids: [t] }))
      }
    }
  }
  walk(list, t => t)
  return lines
}

/** Test hooks. */
export const testing = {
  reset(imp?: typeof importer) {
    ready = null
    loading = null
    cache.clear()
    if (imp) importer = imp
  },
}
