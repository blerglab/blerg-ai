// Where a Blerg Board card opens: the Board app's own page for it, in Blerg (never a link out).
// The Board's address is derived the way coreOrigin derives core's (authClient.ts), for the two
// shapes this repository ships: board.<domain> beside runner.<domain> (install/k8s), or the same
// host on the Board's published port (install/desktop, BLERG_PORT_BOARD, default 8082).
// VITE_BOARD_URL (build time) overrides it; VITE_BOARD_PORT overrides the port.

export function boardOrigin(loc: Pick<Location, 'protocol' | 'hostname' | 'origin'> = location): string {
  const configured = import.meta.env.VITE_BOARD_URL as string | undefined
  if (configured) return configured.replace(/\/+$/, '')
  const { protocol, hostname } = loc
  const runner = hostname.match(/^runner\.(.+)$/)
  if (runner) return `${protocol}//board.${runner[1]}`
  const isBareHost = !hostname.includes('.') || /^\d+(?:\.\d+){3}$/.test(hostname)
  if (isBareHost) return `${protocol}//${hostname}:${(import.meta.env.VITE_BOARD_PORT as string | undefined) || '8082'}`
  return loc.origin
}

/** The Board page of card `number` on board `boardId`. */
export function boardCardUrl(boardId: string, number: number, loc?: Pick<Location, 'protocol' | 'hostname' | 'origin'>): string {
  return `${boardOrigin(loc)}/boards/${encodeURIComponent(boardId)}/cards/${number}`
}
