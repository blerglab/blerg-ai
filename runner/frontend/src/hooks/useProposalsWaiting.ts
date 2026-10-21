import { useEffect, useMemo, useState } from 'react'
import { proposalsWaiting } from '../lib/proposalAttention'
import type { ProposalInfo } from '../types'

// setTimeout's longest delay; a deadline further away is waited for in steps.
const MAX_DELAY = 2 ** 31 - 1

// useProposalsWaiting is proposals without the ones past their deadline (proposalsWaiting), by the
// server's clock: clockOffset is this client's clock minus the server's (serverClockOffset). It
// renders again when the next deadline passes, not on a ticker, so a large page can count with it.
// Every count of pending proposals in the app goes through it or proposalsWaiting, so they agree.
export function useProposalsWaiting(proposals: ProposalInfo[], clockOffset = 0): ProposalInfo[] {
  const [now, setNow] = useState(() => Date.now())
  const waiting = useMemo(() => proposalsWaiting(proposals, now - clockOffset), [proposals, now, clockOffset])
  useEffect(() => {
    const next = Math.min(...waiting.map(p => Date.parse(p.expires_at)).filter(Number.isFinite))
    if (!Number.isFinite(next)) return
    const t = setTimeout(() => setNow(Date.now()), Math.min(Math.max(next - (Date.now() - clockOffset), 0) + 1, MAX_DELAY))
    return () => clearTimeout(t)
  }, [waiting, clockOffset])
  return waiting
}
