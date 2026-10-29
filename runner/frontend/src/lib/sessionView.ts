// sessionView: a session's cached panel data (sessionViewCache) under the signed-in account.
import { signedInAccount } from './liveBoardCard'
import { readView, viewWriter } from './sessionViewCache'

export interface SessionView<T> {
  /** What was kept for this session and slot, if anything. */
  cached: T | undefined
  /** Keeps a value this read returned: only under the account, cache epoch and order this was made
   *  in (sessionViewCache.viewWriter), so make it before the request starts. */
  keep: (value: T) => boolean
}

/** The cached value and a keeper bound to the account signed in now: make one before a read, so
 *  its reply is filed under the account it was read with. */
export function sessionView<T>(sessionId: string, slot: string): SessionView<T> {
  const who = signedInAccount()
  const cached = readView<T>(who, sessionId, slot)
  return { cached, keep: viewWriter<T>(who, sessionId, slot) }
}
