import type { FilterState } from './BoardFilterBar'

/** Apply a FilterState to a list of tickets, returning the passing ones. */
export function applyFilter<T extends { tags: string[]; priority: string; size: string | null }>(
  tickets: T[],
  filter: FilterState,
): T[] {
  return tickets.filter((t) => {
    if (filter.tags.length > 0 && !filter.tags.some((ft) => t.tags.includes(ft))) return false
    if (filter.priorities.length > 0 && !filter.priorities.includes(t.priority)) return false
    if (filter.sizes.length > 0 && (t.size === null || !filter.sizes.includes(t.size))) return false
    return true
  })
}
