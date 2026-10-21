import { useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'

// Where a Blerg Board card opens: the Board app's own page for it, in Blerg (never a link out).
// Core's site metadata names its Board URL; the runner reads it server-side and serves it on its
// own origin (GET /api/site-board), because core sends no CORS headers and this app runs on
// another origin. Deployment-shape inference is a fallback.
// The fallback address is derived the way coreOrigin derives core's (authClient.ts), for the two
// shapes this repository ships: board.<domain> beside runner.<domain> (install/k8s), or the same
// host on the Board's published port (install/desktop, BLERG_PORT_BOARD, default 8082).
// VITE_BOARD_URL (build time) overrides it; VITE_BOARD_PORT overrides the port.

let discovered: string | undefined
let discovery: Promise<string> | undefined
// A failed lookup is remembered this long, so many card links on a page do not each ask again.
const RETRY_AFTER_MS = 60_000
let failedAt = 0

export function loadBoardOrigin(): Promise<string> {
  const configured = import.meta.env.VITE_BOARD_URL as string | undefined
  if (configured) return Promise.resolve(configured.replace(/\/+$/, ''))
  if (discovery && failedAt && Date.now() - failedAt >= RETRY_AFTER_MS) { discovery = undefined; failedAt = 0 }
  if (!discovery) {
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), 8000)
    discovery = (async () => {
      try {
        const response = await apiFetch('/api/site-board', { signal: controller.signal })
        if (!response.ok) throw new Error('Site unavailable')
        const site = await response.json() as { board_url?: unknown }
        if (typeof site.board_url !== 'string' || !site.board_url) throw new Error('Board unavailable')
        const url = new URL(site.board_url)
        if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) throw new Error('Invalid board URL')
        discovered = url.href.replace(/\/+$/, '')
        return discovered
      } catch { failedAt = Date.now(); return boardOrigin() }
      finally { clearTimeout(timer) }
    })()
  }
  return discovery
}

/** Test hook: forget a discovered or failed lookup. */
export function resetBoardOriginForTest(): void { discovered = undefined; discovery = undefined; failedAt = 0 }

export function useBoardOrigin(): string {
  const [base, setBase] = useState(() => boardOrigin())
  useEffect(() => { let active = true; void loadBoardOrigin().then(value => { if (active) setBase(value) }); return () => { active = false } }, [])
  return base
}

export function boardOrigin(loc: Pick<Location, 'protocol' | 'hostname' | 'origin'> = location): string {
  const configured = import.meta.env.VITE_BOARD_URL as string | undefined
  if (configured) return configured.replace(/\/+$/, '')
  if (loc === location && discovered) return discovered
  const { protocol, hostname } = loc
  const runner = hostname.match(/^runner\.(.+)$/)
  if (runner) return `${protocol}//board.${runner[1]}`
  const isBareHost = !hostname.includes('.') || /^\d+(?:\.\d+){3}$/.test(hostname)
  if (isBareHost) return `${protocol}//${hostname}:${(import.meta.env.VITE_BOARD_PORT as string | undefined) || '8082'}`
  return loc.origin
}

/** The Board page of card `number` on board `boardId`. */
export function boardCardUrl(boardId: string, number: number, loc?: string | Pick<Location, 'protocol' | 'hostname' | 'origin'>): string {
  return `${typeof loc === 'string' ? loc : boardOrigin(loc)}/boards/${encodeURIComponent(boardId)}/cards/${number}`
}
