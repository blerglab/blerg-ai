import { useEffect, useMemo, useState } from 'react'
import { apiFetch } from '../apiFetch'
import { getAccessToken } from '../authClient'
import { tokenSubject } from '../claims'
import type { CardTimeSummary } from './cardTime'

// Reads a card's worker-time/review-round summary (BLG-444) from the runner's
// GET /api/board-cards/{board}/{number}/card-time (card_time_summary.go). A card under budget
// answers {} — no fields — which this module treats as "no summary," not a zero: the glyph in
// SessionRail/PlanChecklist is then absent, matching "under budget shows nothing, not 0h."
//
// H3: reads are kept per account and forgotten when the tab's account changes (a silent renewal
// can sign in someone else without a reload), the same pattern liveBoardCard.ts uses — so one
// person never sees a card-time total read under another's sign-in.

interface held { at: number; summary: CardTimeSummary | null }

const reads = new Map<string, held>()
const inflight = new Map<string, Promise<CardTimeSummary | null>>()
const listeners = new Set<() => void>()
const FRESH_MS = 60_000

let heldFor = ''
function account(): string {
  const who = tokenSubject(getAccessToken())
  if (who !== heldFor) {
    reads.clear()
    inflight.clear()
    heldFor = who
  }
  return who
}

window.addEventListener('blerg-identity-changed', () => {
  account()
  // Without this, a mounted useCardTimeSummaries whose `list`/`tick` dependencies haven't
  // otherwise changed would keep its stale memoized Map after the switch (account() alone only
  // clears the module-level stores; it does not by itself cause any component to re-render).
  for (const l of listeners) l()
})

/** The public key a caller (SessionRail, PlanChecklist) looks results up by — never account-
 *  prefixed, since the caller never sees another account's reads in the same tab (account() below
 *  clears on any identity change before this key could be reused across accounts). */
function keyOf(board: string, number: number): string { return `${board}#${number}` }
/** The internal store key: account-scoped, so a read started just before an identity change can
 *  never be attributed to the account that follows it. */
function storeKeyOf(board: string, number: number): string { return `${account()}|${board}#${number}` }

/** Test-only: clears every held read and in-flight request between tests sharing this module. */
export function resetCardTimeDataForTest(): void { reads.clear(); inflight.clear(); heldFor = '' }

async function parse(res: Response): Promise<CardTimeSummary | null> {
  if (!res.ok) return null
  let body: unknown
  try { body = await res.json() } catch { return null }
  const r = body as Partial<CardTimeSummary> | null
  if (!r || typeof r.seconds !== 'number') return null
  return {
    seconds: r.seconds,
    rounds: typeof r.rounds === 'number' ? r.rounds : 0,
    verdict: typeof r.verdict === 'string' ? r.verdict : undefined,
    openHigh: typeof r.openHigh === 'number' ? r.openHigh : (r as Record<string, unknown>).open_high as number | undefined,
    openMedium: typeof r.openMedium === 'number' ? r.openMedium : (r as Record<string, unknown>).open_medium as number | undefined,
  }
}

/** Reads board#number's card-time summary now (a read in flight is shared). */
export function readCardTime(board: string, number: number): Promise<CardTimeSummary | null> {
  const key = storeKeyOf(board, number)
  const running = inflight.get(key)
  if (running) return running
  const p = apiFetch(`/api/board-cards/${encodeURIComponent(board)}/${number}/card-time`).then(parse).then(summary => {
    if (key.startsWith(`${account()}|`)) { // a read under an account the tab no longer holds
      reads.set(key, { at: Date.now(), summary })
      for (const l of listeners) l()
    }
    return summary
  }).catch(() => null).finally(() => inflight.delete(key))
  inflight.set(key, p)
  return p
}

function refreshStale(refs: readonly { board: string; number: number }[]): void {
  for (const r of refs) {
    const held = reads.get(storeKeyOf(r.board, r.number))
    if (held && Date.now() - held.at < FRESH_MS) continue
    readCardTime(r.board, r.number)
  }
}

/** The card-time summary for every (board,number) in refs, fetched and kept briefly fresh.
 *  Returns a function keyed the same way PlanChecklist's cardRefs/SessionRail's pin keys are. */
export function useCardTimeSummaries(refs: readonly { board: string; number: number }[]): Map<string, CardTimeSummary> {
  const list = refs.map(r => keyOf(r.board, r.number)).join(',')
  const [tick, setTick] = useState(0)
  useEffect(() => {
    const l = () => setTick(t => t + 1)
    listeners.add(l)
    return () => { listeners.delete(l) }
  }, [])
  // A pure read (never account()'s own mutating clear-on-switch check, which belongs in an effect
  // or a call made while actually fetching) so a value change — an identity switch between
  // renders — is itself a dependency below, not just something handled silently at module scope.
  const currentAccount = tokenSubject(getAccessToken())
  useEffect(() => {
    refreshStale(refs)
    // refs is read through `list`; currentAccount re-fetches right after an identity switch
    // rather than leaving the panel showing nothing (or another account's cleared-out gap) until
    // something else happens to change `list`.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [list, currentAccount])
  // The existing 60 s freshness window (FRESH_MS) was previously only re-checked when `list`
  // itself changed identity (a pin or checklist ref list edit). A card that crosses its first
  // budget threshold while the panel sits open, with no other edit happening, would then never
  // get picked up. Poll on that same schedule too, so "the plan or pins change" and "60 s have
  // passed" are both covered.
  useEffect(() => {
    const timer = setInterval(() => refreshStale(refs), FRESH_MS)
    return () => clearInterval(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [list, currentAccount])
  return useMemo(() => {
    const out = new Map<string, CardTimeSummary>()
    for (const k of list ? list.split(',') : []) {
      const i = k.lastIndexOf('#')
      const held = reads.get(storeKeyOf(k.slice(0, i), Number(k.slice(i + 1))))
      if (held?.summary) out.set(k, held.summary)
    }
    return out
    // tick: a read landed. currentAccount: an identity switch must drop stale results even if
    // neither `list` nor `tick` otherwise changed this render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [list, tick, currentAccount])
}
