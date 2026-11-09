import type { Data } from '../components/BlackboardTabs'
// Complete views only: a revisit must never inherit half-loaded card counts.
const views = new Map<string, Data>()
let generation = 0
export const blackboardViewKey = (blackboardId: string, query: string) => JSON.stringify([blackboardId, query])
export const cachedBlackboardView = (key: string) => views.get(key) ?? null
export const blackboardViewGeneration = () => generation
export function cacheBlackboardView(key: string, data: Data, started: number): void {
 if (started !== generation) return
 views.delete(key)
 views.set(key, data)
 if (views.size > 20) views.delete(views.keys().next().value!)
}
export function invalidateBlackboardViews(sessionIds?: Iterable<string>): void {
 generation++
 if (!sessionIds) { views.clear(); return }
 const queries = new Set(Array.from(sessionIds, id => `?session=${encodeURIComponent(id)}`))
 for (const key of views.keys()) if (queries.has(JSON.parse(key)[1])) views.delete(key)
}
window.addEventListener('blerg-identity-changed', () => invalidateBlackboardViews())
