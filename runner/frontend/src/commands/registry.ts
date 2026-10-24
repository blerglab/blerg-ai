export type PaletteMode = 'commands' | 'files'
export interface PaletteItem {
  id: string
  title: string
  detail?: string
  keywords?: string
  shortcut?: string
  run(): void
}
export interface SearchResults { items: PaletteItem[]; note?: string }
export interface PaletteProvider {
  id: string
  mode: PaletteMode
  search(query: string, signal: AbortSignal): Promise<SearchResults>
}

/** Features own their registrations and dispose them when their surface goes away. */
export class CommandRegistry {
  private entries = new Map<symbol, { items: PaletteItem[]; providers: PaletteProvider[] }>()
  private listeners = new Set<() => void>()
  private snapshot = { items: [] as PaletteItem[], providers: [] as PaletteProvider[] }
  private recent: string[] = []
  subscribe = (listener: () => void) => { this.listeners.add(listener); return () => { this.listeners.delete(listener) } }
  getSnapshot = () => this.snapshot
  register(items: PaletteItem[], providers: PaletteProvider[] = []) {
    const key = Symbol()
    this.entries.set(key, { items, providers })
    this.changed()
    return () => { this.entries.delete(key); this.changed() }
  }
  remember(id: string) { this.recent = [id, ...this.recent.filter(x => x !== id)].slice(0, 100) }
  rank(items: PaletteItem[], query: string): PaletteItem[] {
    const unique = [...new Map(items.map(item => [item.id, item])).values()]
    return unique.map(item => ({ item, score: fuzzyScore(`${item.title} ${item.detail ?? ''} ${item.keywords ?? ''}`, query), recent: this.recent.indexOf(item.id) }))
      .filter(row => row.score !== null)
      .sort((a, b) => (a.recent < 0 ? Infinity : a.recent) - (b.recent < 0 ? Infinity : b.recent) || a.score! - b.score! || a.item.title.localeCompare(b.item.title))
      .map(row => row.item)
  }
  private changed() {
    const entries = [...this.entries.values()]
    this.snapshot = { items: entries.flatMap(e => e.items), providers: entries.flatMap(e => e.providers) }
    this.listeners.forEach(listener => listener())
  }
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

export function paletteShortcut(event: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey' | 'isComposing' | 'repeat'>, pathname: string): PaletteMode | null {
  if (event.isComposing || event.repeat || event.altKey || event.shiftKey || !(event.ctrlKey || event.metaKey)) return null
  // Board owns both / and Ctrl/Command+K, including its card and archive routes.
  if (/^\/boards(?:\/|$)/.test(pathname)) return null
  if (event.key.toLowerCase() === 'k') return 'commands'
  if (event.key.toLowerCase() === 'p' && /^\/sessions\/[^/]+\/?$/.test(pathname)) return 'files'
  return null
}
