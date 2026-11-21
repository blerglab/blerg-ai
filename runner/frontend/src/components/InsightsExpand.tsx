// The control for a capped Insights list (the hook is useExpand in hooks/useExpand.ts).

// Named to not collide with a neighboring "Show all" that clears a chart selection (BarChart,
// SpreadChart): that one has no count and means something different (return to every bar).
export function ShowAllButton({ id, total, limit, expanded, onToggle }: { id: string; total: number; limit: number; expanded: boolean; onToggle: () => void }) {
  return <button type="button" className="insights-sort" aria-expanded={expanded} aria-controls={id} onClick={onToggle}>{expanded ? `Show top ${limit}` : `See all ${total}`}</button>
}
