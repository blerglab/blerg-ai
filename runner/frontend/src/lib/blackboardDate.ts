// Milestone targets are calendar dates, not times in the viewer's timezone.
export function blackboardTargetDate(value: string, locale?: string): string {
 return new Date(value).toLocaleDateString(locale, { timeZone: 'UTC', month: 'short', day: 'numeric' })
}
