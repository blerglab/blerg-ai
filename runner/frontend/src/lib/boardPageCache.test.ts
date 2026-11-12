import { beforeEach, expect, it } from 'vitest'
import { boardPageCacheKey, cachedBoardPage, clearBoardPageCache, rememberBoardPage } from './boardPageCache'
beforeEach(clearBoardPageCache)
const page = { columns: [], cards: [] }
it('reuses only the same account, workspace, board and filter variant', () => {
 const key = boardPageCacheKey('a', 'workspace', 'board')
 rememberBoardPage(key, page, 'query', 'release', 123)
 expect(cachedBoardPage(key, 'query', 'release')?.data).toBe(page)
 expect(cachedBoardPage(key, '', 'release')).toBeUndefined()
 expect(cachedBoardPage(boardPageCacheKey('b', 'workspace', 'board'), 'query', 'release')).toBeUndefined()
 expect(cachedBoardPage(boardPageCacheKey('a', 'other', 'board'), 'query', 'release')).toBeUndefined()
})
it('bounds memory while retaining recently used boards', () => {
 for (let i = 0; i < 12; i++) rememberBoardPage(String(i), page, '', '', i)
 cachedBoardPage('0', '', '')
 rememberBoardPage('12', page, '', '', 12)
 expect(cachedBoardPage('0', '', '')).toBeTruthy()
 expect(cachedBoardPage('1', '', '')).toBeUndefined()
 clearBoardPageCache()
 expect(cachedBoardPage('0', '', '')).toBeUndefined()
})
