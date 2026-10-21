import type { ProposalInfo } from '../types'

export const PROPOSAL_WARNING_MS = 24 * 60 * 60_000
export const PROPOSAL_EXTENSION_MS = 7 * 24 * 60 * 60_000
export const PROPOSAL_CAP_REASON = 'Proposals can stay open up to 30 days.'

// What Extend does to p: nothing once the 30-day cap is reached, a shorter step to reach it
// exactly, or the full seven days.
export function proposalExtension(p: ProposalInfo): { allowed: boolean; label: string } {
  if (p.extendable === false) return { allowed: false, label: 'Extend 7 days' }
  const room = p.max_expires_at ? Date.parse(p.max_expires_at) - Date.parse(p.expires_at) : Infinity
  return { allowed: true, label: room < PROPOSAL_EXTENSION_MS ? 'Extend to the 30-day limit' : 'Extend 7 days' }
}
// A pending proposal past its deadline that the server's sweeper has not yet moved to expired. It
// is shown as expired (it cannot be approved or extended), so it no longer needs the person, and no
// count includes it: the server's counts leave it out too. An unreadable deadline is not overdue.
export function proposalOverdue(p: ProposalInfo, now: number) {
  const deadline = Date.parse(p.expires_at)
  return p.state === 'pending' && Number.isFinite(deadline) && deadline <= now
}
// serverClockOffset is this client's clock minus the server's (ms), from the server_time a proposals
// list was sent with: overdue is decided at Date.now() - offset, the server's now. 0 when the server
// sent none (an older one) or it cannot be read.
export function serverClockOffset(serverTime: unknown, clientNow = Date.now()): number {
  const at = typeof serverTime === 'string' ? Date.parse(serverTime) : NaN
  return Number.isFinite(at) ? clientNow - at : 0
}
// The proposals that still wait on the person: every one but the overdue.
export function proposalsWaiting(ps: ProposalInfo[], now: number) {
  return ps.filter(p => !proposalOverdue(p, now))
}
export function proposalExpiring(p: ProposalInfo, now: number) {
  const left = Date.parse(p.expires_at) - now
  return p.state === 'pending' && left > 0 && left <= PROPOSAL_WARNING_MS
}

// Who did something in a proposal's history. Every proposal the page shows is the viewer's own, and
// the runner knows no account names, so it reads "you"; an account id is never shown.
export function actorLabel(byYou: boolean | undefined) {
  return byYou === false ? 'another account' : 'you'
}

const createdVia: Record<string, string> = { card: 'from a proposal card', panel: 'in the session’s Permissions', launch: 'at launch' }
export function permissionVia(via: string) { return createdVia[via] ?? via }

// Deadlines already warned about, by "<id>:<deadline>", for the whole page: the header's bell and a
// session page's bell (remounted on every session switch) share it, so each deadline warns once.
export const warnedProposalDeadlines = new Set<string>()
export function resetProposalWarnings() { warnedProposalDeadlines.clear() }
