import type { LiveBoardCard, LiveBoardColumn } from './liveBoardCard'

export type BoardPage = { releases?: { id: string; name: string; status: string }[]; columns: LiveBoardColumn[]; cards: LiveBoardCard[]; next_cursor?: string }
type CachedBoard = { data: BoardPage; query: string; release: string; loadedAt: number }
const pages = new Map<string, CachedBoard>()
const limit = 12

// Account and workspace scope prevent reuse across sign-ins or different workspace grants.
// Keep only the latest filter variant per board, bounded across workspace navigation.
export const boardPageCacheKey = (account: string, session: string, board: string) => JSON.stringify([account, session, board])
export function cachedBoardPage(key: string, query: string, release: string): CachedBoard | undefined {
 const page = pages.get(key)
 if (!page || page.query !== query || page.release !== release) return
 pages.delete(key); pages.set(key, page)
 return page
}
export function rememberBoardPage(key: string, data: BoardPage, query: string, release: string, loadedAt: number) {
 pages.delete(key); pages.set(key, { data, query, release, loadedAt })
 while (pages.size > limit) pages.delete(pages.keys().next().value!)
}
export function clearBoardPageCache() { pages.clear() }
