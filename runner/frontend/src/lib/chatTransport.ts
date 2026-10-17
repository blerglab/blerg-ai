// One transport for the page: the runner's shared socket and its browser token. A token that
// looks expired is renewed before a request; a route that still answers 401 sends the page to
// core's refresh, as apiFetch does. The chat and the session panel (a diff's counts) share it.
import { createBlergTransport, type Transport } from '@blerglab/chat'
import * as ws from '../ws'
import { ensureFreshToken, getAccessToken } from '../authClient'
import { bounceToRefresh } from '../signIn'
import { tokenExpired } from '../claims'

let shared: Transport | null = null
export function blergTransport(): Transport {
  if (shared) return shared
  shared = createBlergTransport({
    socket: ws,
    getToken: async () => {
      const tok = getAccessToken()
      if (!tok || tokenExpired(tok)) await ensureFreshToken()
      return getAccessToken()
    },
    onUnauthorized: () => {
      bounceToRefresh()
    },
  })
  return shared
}
