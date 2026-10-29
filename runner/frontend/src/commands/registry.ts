import type { ReactNode } from 'react'
export type PaletteMode = 'commands' | 'files'
export type PaletteGroup = 'sessions' | 'cards' | 'files' | 'commands'
export const paletteGroups: PaletteGroup[] = ['sessions', 'cards', 'files', 'commands']
export const groupLabels: Record<PaletteGroup, string> = { sessions: 'Sessions', cards: 'Cards', files: 'Files', commands: 'Commands' }
export function parseScope(value: string): { scope: PaletteGroup | null; query: string } {
  const match = value.match(/^(session:|s |#|\/|>)\s*/i)
  if (!match) return { scope: null, query: value }
  return { scope: ({ 'session:': 'sessions', 's ': 'sessions', '#': 'cards', '/': 'files', '>': 'commands' } as Record<string, PaletteGroup>)[match[1].toLowerCase()], query: value.slice(match[0].length) }
}
export interface PaletteItem {
  id: string
  title: string
  group?: PaletteGroup
  repo?: string
  /** Includes consent, permission changes, and destructive actions: never default or recent. */
  destructive?: boolean
  empty?: boolean
  recentAt?: string
  /** An action on the session in view: listed, in this order, in the empty palette's This session group. */
  here?: number
  /** A session the empty palette offers under Recent sessions, in this order. */
  recentRank?: number
  /** Choosing it searches for this text instead of running; offered only in the empty palette. */
  refine?: string
  /** Board search already ranks body matches; do not discard them by title. */
  searchMatched?: boolean
  icon?: ReactNode
  detail?: string
  keywords?: string
  shortcut?: string
  run(): void
}
/** Every feature action must make an explicit safety decision. */
export type PaletteCommand = PaletteItem & { destructive: boolean }
export function assertCommandSafety(items: PaletteItem[]) {
  for (const item of items) {
    if (typeof item.destructive !== 'boolean') throw new Error(`Palette action ${item.id} must explicitly set destructive for consent, permission changes, or destruction`)
  }
}
export interface SearchResults { items: PaletteItem[]; note?: string }
export interface PaletteProvider {
  id: string
  mode: PaletteGroup
  search(query: string, signal: AbortSignal): Promise<SearchResults>
}

/** Features own their registrations and dispose them when their surface goes away. */
export class CommandRegistry {
  private entries = new Map<symbol, { items: PaletteItem[]; providers: PaletteProvider[]; actions: Map<string, () => void> }>()
  private listeners = new Set<() => void>()
  private snapshot = { items: [] as PaletteItem[], providers: [] as PaletteProvider[] }
  private recent: string[] = []
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  getSnapshot = () => this.snapshot
  register(items: PaletteItem[], providers: PaletteProvider[] = []) {
    const key = Symbol()
    const entry = { items: [] as PaletteItem[], providers }
    const update = (items: PaletteItem[], providers: PaletteProvider[] = []) => {
      const previous = new Map(entry.items.map(item => [item.id, item]))
      // Keep display objects stable but always execute the feature's latest callback.
      entry.items = items.map(item => {
        const old = previous.get(item.id)
        if (old && sameResult(old, item)) return old
        return { ...item, run: () => this.entries.get(key)?.actions.get(item.id)?.() }
      })
      entry.providers = providers
      this.entries.set(key, { ...entry, actions: new Map(items.map(item => [item.id, item.run])) })
      this.changed()
    }
    update(items, providers)
    return Object.assign(() => { this.entries.delete(key); this.changed() }, { update })
  }

  remember(id: string) { this.recent = [id, ...this.recent.filter(x => x !== id)].slice(0, 100) }
  rank(items: PaletteItem[], query: string): PaletteItem[] {
    const unique = [...new Map(items.map(item => [item.id, item])).values()]
    return unique.map(item => ({ item, score: matchScore(item, query), recent: item.destructive ? -1 : this.recent.indexOf(item.id) }))
      .filter(row => row.score !== null)
      .sort((a, b) => Number(!!a.item.destructive) - Number(!!b.item.destructive) || a.score! - b.score! ||
        (a.recent < 0 ? Infinity : a.recent) - (b.recent < 0 ? Infinity : b.recent) ||
        (!query.trim() && !a.item.destructive && !b.item.destructive ? (b.item.recentAt ?? '').localeCompare(a.item.recentAt ?? '') : 0) || a.item.title.localeCompare(b.item.title))
      .map(row => row.item)
  }
  private changed() {
    const entries = [...this.entries.values()]
    const items = entries.flatMap(e => e.items), providers = entries.flatMap(e => e.providers)
    const sameItems = sameArray(this.snapshot.items, items), sameProviders = sameArray(this.snapshot.providers, providers)
    if (sameItems && sameProviders) return
    this.snapshot = { items: sameItems ? this.snapshot.items : items, providers: sameProviders ? this.snapshot.providers : providers }
    this.listeners.forEach(listener => listener())
  }
}

function sameArray<T>(a: T[], b: T[]) { return a.length === b.length && a.every((value, i) => value === b[i]) }
function sameResult(a: PaletteItem, b: PaletteItem) {
  return a.icon === b.icon && a.title === b.title && a.detail === b.detail && a.keywords === b.keywords && a.shortcut === b.shortcut && a.group === b.group && a.repo === b.repo && a.destructive === b.destructive && a.empty === b.empty && a.recentAt === b.recentAt && a.searchMatched === b.searchMatched && a.here === b.here && a.recentRank === b.recentRank && a.refine === b.refine
}

/** Separate fields prevent incidental detail/keyword text from outranking a name. */
function nameScore(text: string, query: string): number | null {
  const value = text.toLocaleLowerCase(), needle = query.trim().toLocaleLowerCase()
  if (!needle || value === needle) return 0
  if (value.startsWith(needle)) return 10
  if (value.includes(needle)) return 20
  const fuzzy = fuzzyScore(text, query)
  return fuzzy === null ? null : 30 + Math.min(60, Math.floor(fuzzy / 4))
}
function matchScore(item: PaletteItem, query: string): number | null {
  const title = nameScore(item.title, query)
  if (title !== null) return title
  if (item.group === 'sessions') {
    const repo = nameScore(item.repo ?? '', query)
    if (repo !== null) return 100 + repo
  }
  const detail = nameScore(`${item.detail ?? ''} ${item.keywords ?? ''}`, query)
  return detail === null ? (item.searchMatched ? 300 : null) : 200 + detail
}

/** Subsequence matching; contiguous matches and word boundaries rank ahead of gaps. */
export function fuzzyScore(text: string, query: string): number | null {
  const value = text.toLocaleLowerCase()
  const needle = query.trim().toLocaleLowerCase()
  let position = 0, score = 0
  for (const char of needle) {
    if (char === ' ') continue
    const next = value.indexOf(char, position)
    if (next < 0) return null
    score += next - position + (next === 0 || /[\s/_.-]/.test(value[next - 1]) ? 0 : 2)
    position = next + 1
  }
  return score
}

/** Match owned keys, including repeats; the caller decides whether to open search. */
export function paletteShortcut(event: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey' | 'isComposing' | 'repeat'>, pathname: string): PaletteMode | null {
  if (event.isComposing || event.altKey || event.shiftKey || !(event.ctrlKey || event.metaKey)) return null
  // Board owns both / and Ctrl/Command+K, including its card and archive routes.
  if (/^\/boards(?:\/|$)/.test(pathname)) return null
  if (event.key.toLowerCase() === 'k') return 'commands'
  if (event.key.toLowerCase() === 'p' && /^\/sessions\/[^/]+\/?$/.test(pathname)) return 'files'
  return null
}
