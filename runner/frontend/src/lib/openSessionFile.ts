// The global palette delegates to the mounted session's tab host, when present.
import { viewEpoch } from './sessionViewCache'

export const OPEN_SESSION_FILE = 'blerg-open-session-file'
export const REVEAL_SESSION_FILE = 'blerg-reveal-session-file'
export interface SessionFileRequest { session: string; account: string; path: string }
let revealSequence = 0
export interface SessionFileReveal extends SessionFileRequest { version: number; handled?: boolean }
const revealRequests = new Map<string, SessionFileReveal>()
let revealEpoch = viewEpoch()
const revealKey = (account: string, session: string) => JSON.stringify([account, session])
function currentRequests(): typeof revealRequests {
  const epoch = viewEpoch()
  if (epoch !== revealEpoch) { revealRequests.clear(); revealEpoch = epoch }
  return revealRequests
}
/** A hidden explorer can distinguish a new reference from the file it revealed before hiding. */
export function sessionFileRevealVersion(account: string, session: string): number {
  return sessionFileRevealRequest(account, session)?.version ?? 0
}
export function sessionFileRevealRequest(account: string, session: string): SessionFileReveal | undefined {
  return currentRequests().get(revealKey(account, session))
}
export function completeSessionFileReveal(account: string, session: string, version: number): void {
  const request = sessionFileRevealRequest(account, session)
  if (request?.version === version) request.handled = true
}
export function openSessionFile(request: SessionFileRequest): boolean {
  return !window.dispatchEvent(new CustomEvent(OPEN_SESSION_FILE, { cancelable: true, detail: request }))
}
/** Opening an already active file still reveals it after its parent was collapsed. */
export function revealSessionFile(request: SessionFileRequest): void {
  const key = revealKey(request.account, request.session)
  const requests = currentRequests()
  requests.delete(key)
  requests.set(key, { ...request, version: ++revealSequence })
  if (requests.size > 100) requests.delete(requests.keys().next().value!)
  window.dispatchEvent(new CustomEvent(REVEAL_SESSION_FILE, { detail: request }))
}
