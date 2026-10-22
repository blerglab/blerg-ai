// One transport for the page: the runner's shared socket and its browser token. A token that
// looks expired is renewed before a request; a route that still answers 401 sends the page to
// core's refresh, as apiFetch does. The chat and the session panel (a diff's counts) share it.
import { createBlergTransport, type Transport } from '@blerglab/chat'
import * as ws from '../ws'
import { ensureFreshToken, getAccessToken } from '../authClient'
import { bounceToRefresh } from '../signIn'
import { tokenExpired, tokenSubject } from '../claims'
import { checkAccount } from './liveBoardCard'
import { notifyReviewRequestsChanged } from '../hooks/useMyReviewRequests'

let shared: Transport | null = null
export function blergTransport(): Transport {
  if (shared) return shared
  shared = createBlergTransport({
    socket: ws,
    getToken: async () => {
      const tok = getAccessToken()
      if (!tok || tokenExpired(tok)) await ensureFreshToken()
      // A renewal may have signed in someone else: what was read under the other account goes.
      checkAccount()
      return getAccessToken()
    },
    onUnauthorized: () => {
      bounceToRefresh()
    },
    // Who is signed in: a question someone else dismissed says so (not "dismissed by you").
    viewerId: () => tokenSubject(getAccessToken()),
  })
  // A review decided in the viewer leaves the bell at once, not at its next minute's read.
  const decide = shared.decideReview?.bind(shared)
  if (decide) {
    shared.decideReview = async (...args) => {
      try {
        return await decide(...args)
      } finally {
        notifyReviewRequestsChanged()
      }
    }
  }
  return shared
}
