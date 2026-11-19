// The mounted workspace owns its panel set; navigation falls back to the legacy route.
export const OPEN_INSIGHTS = 'blerg-open-insights'
export function openInsights(): boolean {
  return !window.dispatchEvent(new Event(OPEN_INSIGHTS, { cancelable: true }))
}
