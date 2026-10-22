// References in chat text (card #153): a board card number (#147) and a commit hash (08025f2c) in a
// message become the same chips as elsewhere (a hovercard on hover, a click opens the ticket or the
// commit viewer), but only once confirmed:
//   - `#N` only when the host knows the session's board and the card exists there (its card read);
//   - a hash (7 to 40 hex digits, at least one a digit) only when the session's repository says it
//     is a commit (the transport's commit read, which only the session's owner may make).
// Until then the text is drawn exactly as it is, so nothing moves. Lookups are batched: every
// reference asked for within DEBOUNCE_MS goes in one pass, each key at most once per session; the
// answers (yes or no) are kept for the page's life.
import type { CardTarget } from '../components/Hovercard'
import type { Ticket } from '../types'

/** What the host lends the chat for references. */
export interface ChatRefs {
  /** The cards the numbers name on the session's board, those that exist. Absent while the board is
   *  not known (or there is none): numbers wait, and are asked once a reader is given. */
  cards?: (numbers: number[]) => Promise<Map<number, CardTarget>>
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

export type RefKey = `card:${number}` | `commit:${string}`
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
  setResolvers(r: { cards?: ChatRefs['cards']; commit?: ((sha: string) => Promise<string | null>) | null }): void
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
    const numbers: number[] = []
    const shas: string[] = []
    for (const k of wanted) {
      if (known.has(k)) continue
      if (k.startsWith('card:')) { if (cards) numbers.push(Number(k.slice(5))) }
      else if (commit) shas.push(k.slice(7))
      else if (commit === null) settle(k, NONE)
    }
    const jobs: Promise<void>[] = []
    if (numbers.length && cards) {
      for (const n of numbers) known.set(`card:${n}`, PENDING)
      calls.cards++
      const read = cards
      jobs.push(read(numbers).then(
        found => { for (const n of numbers) { const t = found.get(n); settle(`card:${n}`, t ? { state: 'card', target: t } : NONE) } },
        () => { for (const n of numbers) settle(`card:${n}`, NONE) },
      ))
    }
    if (shas.length && commit) {
      const read = commit
      for (const s of shas) known.set(`commit:${s}`, PENDING)
      let i = 0
      const worker = async () => {
        while (i < shas.length) {
          const s = shas[i++]
          calls.commits++
          let full: string | null
          try { full = await read(s) } catch { full = null }
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
    setResolvers(r) {
      const cardsNew = !!r.cards && r.cards !== cards
      cards = r.cards
      commit = r.commit
      // What waited for a reader is asked now.
      if (cardsNew || r.commit !== undefined) {
        for (const k of wanted) if (!known.has(k)) { schedule(); break }
      }
    },
  }
}

// One store per session for the page's life: a reference is asked once, whatever is remounted.
const stores = new Map<string, RefStore>()
export function refStoreFor(sessionId: string): RefStore {
  let s = stores.get(sessionId)
  if (!s) { s = createRefStore(); stores.set(sessionId, s) }
  return s
}
export function resetRefStoresForTest(): void {
  stores.clear()
}

/** A piece of text split into plain text and reference candidates, in order. */
export type RefPiece = { text: string } | { text: string; key: RefKey }

const CANDIDATE = /(^|[^\w&#/])#(\d{1,7})(?![\w-])|(^|[^\w/.-])((?=[0-9a-fA-F]*\d)[0-9a-fA-F]{7,40})(?![\w-])/g

/** Splits prose into text and candidates: "#147" (not "&#147", "a#1", "#1a") and a hash word. */
export function splitRefs(text: string): RefPiece[] {
  const out: RefPiece[] = []
  let last = 0
  CANDIDATE.lastIndex = 0
  for (let m = CANDIDATE.exec(text); m; m = CANDIDATE.exec(text)) {
    const lead = m[1] ?? m[3] ?? ''
    const start = m.index + lead.length
    const word = m[2] !== undefined ? `#${m[2]}` : m[4]
    const key: RefKey = m[2] !== undefined ? `card:${Number(m[2])}` : `commit:${m[4].toLowerCase()}`
    if (start > last) out.push({ text: text.slice(last, start) })
    out.push({ text: word, key })
    last = start + word.length
    CANDIDATE.lastIndex = last
  }
  if (last < text.length) out.push({ text: text.slice(last) })
  return out
}
