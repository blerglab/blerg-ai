// References in chat text (card #153): a board card number (#147) and a commit hash (08025f2c) in a
// message become the same chips as elsewhere (a hovercard on hover, a click opens the ticket or the
// commit viewer), but only once confirmed:
//   - `#N` only when the host knows the session's board and the card exists there (its card read);
//   - a hash (7 to 40 hex digits, at least one a digit) only when the session's repository says it
//     is a commit (the transport's commit read, which only the session's owner may make).
// Until then the text is drawn exactly as it is, so nothing moves. Lookups are batched: every
// reference asked for within DEBOUNCE_MS goes in one pass, each key at most once per session; the
// answers (yes or no) are kept until the viewer or the corresponding resolver changes.
import type { CardTarget } from '../components/Hovercard'
import type { Ticket } from '../types'

/** What the host lends the chat for references. */
export interface ChatRefs {
  /** The cards the numbers name on the session's board, those that exist. Absent while the board is
   *  not known (or there is none): numbers wait, and are asked once a reader is given. */
  cards?: (numbers: number[]) => Promise<Map<number, CardTarget>>
  /** Explicit board IDs or board keys (including aliases). The host enforces access when reading the card. */
  boardCards?: (boardId: string, numbers: number[]) => Promise<Map<number, CardTarget>>
  /** Opens a confirmed card (the host's ticket dialog). */
  openCard?: (t: CardTarget) => void
  /** Opens a pinned ticket in the host's ticket dialog (the runner's: the live Board card in the
   *  two-column layout). The chat's pin rows use it; without it they open the chat's own TicketModal
   *  on the pinned copy (a host with no live read, the Board's chat). */
  openTicket?: (ticket: Ticket) => void
  /** Whether this viewer may read commits (the session's owner): otherwise hashes stay text and
   *  nothing is asked. */
  commits?: boolean
}

export type RefKey = `card:${number}` | `card:${string}:${number}` | `commit:${string}`
export type RefState =
  | { state: 'pending' }
  | { state: 'none' }
  | { state: 'card'; target: CardTarget }
  | { state: 'commit'; sha: string }

export const DEBOUNCE_MS = 60
/** Commit reads in flight at once. */
const COMMIT_CONCURRENCY = 4

const HASH = /^(?=[0-9a-fA-F]*\d)[0-9a-fA-F]{7,40}$/
export function isHash(word: string): boolean {
  return HASH.test(word)
}

export interface RefStore {
  get(key: RefKey): RefState
  want(key: RefKey): void
  subscribe(fn: () => void): () => void
  /** Forget this viewer’s answers and readers, including any reads still in flight. */
  clear(): void
  setResolvers(r: { cards?: ChatRefs['cards']; boardCards?: ChatRefs['boardCards']; commit?: ((sha: string) => Promise<string | null>) | null }): void
  /** For tests: how many lookups each kind made. */
  readonly calls: { cards: number; commits: number }
}

const PENDING: RefState = { state: 'pending' }
const NONE: RefState = { state: 'none' }

export function createRefStore(timer: { set: (fn: () => void, ms: number) => unknown } = { set: (fn, ms) => setTimeout(fn, ms) }): RefStore {
  const known = new Map<RefKey, RefState>()
  const wanted = new Set<RefKey>()
  const listeners = new Set<() => void>()
  let scheduled = false
  let cards: ChatRefs['cards'] | undefined
  let boardCards: ChatRefs['boardCards'] | undefined
  let boardRevision = 0
  let qualifiedRevision = 0
  let commitRevision = 0
  let commit: ((sha: string) => Promise<string | null>) | null | undefined
  const calls = { cards: 0, commits: 0 }
  const emit = () => { for (const l of listeners) l() }
  const settle = (k: RefKey, s: RefState) => { known.set(k, s); wanted.delete(k) }

  function schedule() {
    if (scheduled) return
    scheduled = true
    timer.set(() => { scheduled = false; void flush() }, DEBOUNCE_MS)
  }

  async function flush() {
    const readCards = cards, readBoardCards = boardCards, readCommit = commit
    const revision = boardRevision, qualifiedVersion = qualifiedRevision, commits = commitRevision
    const numbers: number[] = []
    const shas: string[] = []
    const qualified = new Map<string, number[]>()
    for (const k of wanted) {
      if (known.has(k)) continue
      if (k.startsWith('card:')) {
        const parts = k.split(':')
        if (parts.length === 3) {
          if (readBoardCards) qualified.set(parts[1], [...(qualified.get(parts[1]) ?? []), Number(parts[2])])
        } else if (readCards) numbers.push(Number(parts[1]))
      }
      else if (readCommit) shas.push(k.slice(7))
      else if (readCommit === null) settle(k, NONE)
    }
    const jobs: Promise<void>[] = []
    if (numbers.length && readCards && revision === boardRevision) {
      for (const n of numbers) known.set(`card:${n}`, PENDING)
      calls.cards++
      jobs.push(readCards(numbers).then(
        found => { if (revision !== boardRevision) return; for (const n of numbers) { const t = found.get(n); settle(`card:${n}`, t ? { state: 'card', target: t } : NONE) } },
        () => { if (revision !== boardRevision) return; for (const n of numbers) settle(`card:${n}`, NONE) },
      ))
    }
    for (const [board, ns] of qualified) {
      if (!readBoardCards || qualifiedVersion !== qualifiedRevision) break
      const key = (n: number): RefKey => `card:${board}:${n}`
      for (const n of ns) known.set(key(n), PENDING)
      calls.cards++
      jobs.push(readBoardCards(board, ns).then(
        found => { if (qualifiedVersion !== qualifiedRevision) return; for (const n of ns) { const t = found.get(n); settle(key(n), t ? { state: 'card', target: t } : NONE) } },
        () => { if (qualifiedVersion !== qualifiedRevision) return; for (const n of ns) settle(key(n), NONE) },
      ))
    }
    if (shas.length && readCommit && commits === commitRevision) {
      for (const s of shas) known.set(`commit:${s}`, PENDING)
      let i = 0
      const worker = async () => {
        while (i < shas.length && commits === commitRevision) {
          const s = shas[i++]
          calls.commits++
          let full: string | null
          try { full = await readCommit(s) } catch { full = null }
          if (commits !== commitRevision) return
          settle(`commit:${s}`, full && full.toLowerCase().startsWith(s.toLowerCase()) ? { state: 'commit', sha: full } : NONE)
          emit()
        }
      }
      jobs.push(...Array.from({ length: Math.min(COMMIT_CONCURRENCY, shas.length) }, worker))
    }
    await Promise.all(jobs)
    emit()
  }

  return {
    calls,
    get: k => (known.get(k) ?? PENDING),
    want(k) {
      if (known.has(k) || wanted.has(k)) return
      wanted.add(k)
      schedule()
    },
    subscribe(fn) { listeners.add(fn); return () => { listeners.delete(fn) } },
    clear() {
      boardRevision++
      qualifiedRevision++
      commitRevision++
      known.clear()
      wanted.clear()
      cards = undefined
      boardCards = undefined
      commit = undefined
      emit()
    },
    setResolvers(r) {
      const cardsNew = r.cards !== cards
      if (cardsNew) {
        boardRevision++
        // A board created or pinned during the session replaces the bare-number context.
        for (const k of known.keys()) if (/^card:\d+$/.test(k)) { known.delete(k); wanted.add(k) }
      }
      const qualifiedNew = r.boardCards !== boardCards
      if (qualifiedNew) {
        qualifiedRevision++
        for (const k of known.keys()) if (/^card:[^:]+:\d+$/.test(k)) { known.delete(k); wanted.add(k) }
      }
      const commitNew = r.commit !== commit
      if (commitNew) {
        commitRevision++
        for (const k of known.keys()) if (k.startsWith('commit:')) { known.delete(k); wanted.add(k) }
      }
      boardCards = r.boardCards
      cards = r.cards
      commit = r.commit
      // What waited for a reader is asked now.
      if (cardsNew || qualifiedNew || commitNew) {
        for (const k of wanted) if (!known.has(k)) { schedule(); break }
      }
      if (cardsNew || qualifiedNew || commitNew) emit()
    },
  }
}

// One store per session for the viewer's life: remounts share answers until identity changes.
const stores = new Map<string, RefStore>()
export function refStoreFor(sessionId: string): RefStore {
  let s = stores.get(sessionId)
  if (!s) { s = createRefStore(); stores.set(sessionId, s) }
  return s
}
/** The host must call this synchronously on account changes, before remounting chat or checklists. */
export function clearRefStores(): void {
  const previous = [...stores.values()]
  stores.clear()
  for (const store of previous) store.clear()
}
export function resetRefStoresForTest(): void {
  clearRefStores()
}

/** A piece of text split into plain text and reference candidates, in order. */
export type RefPiece = { text: string } | { text: string; key: RefKey } | { text: string; cards: number[] }

/** At most ten cards per ascending range, including both endpoints. */
export const MAX_CARD_RANGE = 10

const CANDIDATE = /(^|[^\w&#])#(\d{1,7})([-–][#\w–-]*)?(?![\w-])|(^|[^\w/.-])((?=[0-9a-fA-F]*\d)[0-9a-fA-F]{7,40})(?![\w-])/g

/** Splits prose into text and candidates: "#147" (not "&#147", "a#1", "#1a") and a hash word. */
function splitUnqualifiedRefs(text: string): RefPiece[] {
  const out: RefPiece[] = []
  let last = 0
  CANDIDATE.lastIndex = 0
  for (let m = CANDIDATE.exec(text); m; m = CANDIDATE.exec(text)) {
    const lead = m[1] ?? m[4] ?? ''
    // A slash continues only the card we just accepted, never a path or URL.
    const previous = out.at(-1)
    if (lead === '/' && (m.index !== last || !previous || !('cards' in previous || 'key' in previous && previous.key.startsWith('card:')))) continue
    const start = m.index + lead.length
    const word = m[2] !== undefined ? `#${m[2]}${m[3] ?? ''}` : m[5]
    const key: RefKey = m[2] !== undefined ? `card:${Number(m[2])}` : `commit:${m[5].toLowerCase()}`
    if (start > last) out.push({ text: text.slice(last, start) })
    if (m[3] !== undefined) {
      const endpoint = /^[-–]#?(\d{1,7})$/.exec(m[3])
      const first = Number(m[2]), end = endpoint ? Number(endpoint[1]) : 0
      out.push(first > 0 && end > first && end - first < MAX_CARD_RANGE
        ? { text: word, cards: Array.from({ length: end - first + 1 }, (_, i) => first + i) }
        : { text: word })
    } else out.push({ text: word, key })
    last = start + word.length
    CANDIDATE.lastIndex = last
  }
  if (last < text.length) out.push({ text: text.slice(last) })
  return out
}

/** Canonical KEY-N and legacy board:<board-id>#N forms, in prose or tables. */
export function splitRefs(text: string): RefPiece[] {
  const out: RefPiece[] = []
  const qualified = /(^|[^\w&#-])(?:board:([A-Za-z0-9_-]{1,64})#([1-9]\d{0,6})|([A-Z][A-Z0-9]{1,4})-([1-9]\d{0,6}))(?![\w#-])/g
  let last = 0
  for (const match of text.matchAll(qualified)) {
    const start = match.index! + match[1].length
    if (match[1] === '/' && (match.index !== last || !out.at(-1) || !('key' in out.at(-1)!))) continue
    out.push(...splitUnqualifiedRefs(text.slice(last, start)))
    const word = match[2] ? `board:${match[2]}#${match[3]}` : `${match[4]}-${match[5]}`
    out.push({ text: word, key: `card:${match[2] ?? match[4]}:${Number(match[3] ?? match[5])}` })
    last = start + word.length
  }
  out.push(...splitUnqualifiedRefs(text.slice(last)))
  return out
}
