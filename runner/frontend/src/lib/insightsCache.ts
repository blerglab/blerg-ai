// Match the Board's in-memory redraw, bounded and isolated by signed-in person.
const reports = new Map<string, unknown>()
export const insightsCacheKey = (account: string, scope: string) => JSON.stringify([account, scope])
export const cachedInsights = <T,>(key: string): T | null => reports.get(key) as T ?? null
export function rememberInsights<T>(key: string, value: T) {
 reports.delete(key)
 reports.set(key, value)
 if (reports.size > 24) reports.delete(reports.keys().next().value!)
}
export function clearInsightsCache() { reports.clear() }
window.addEventListener('blerg-identity-changed', clearInsightsCache)
