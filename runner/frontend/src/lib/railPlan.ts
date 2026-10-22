// The session panel's Plan tab: when it shows, what its label says, and the one summary line Live
// keeps at its top. Pure: SessionRail passes in what the chat reports.
import { groupArtifacts, type ArtifactInfo, type ChecklistItem, type PlanLive } from '@blerglab/chat'

/** The session's published diffs, newest first, one per file name (the latest version of each). */
export function diffArtifacts(items: ArtifactInfo[] | undefined): ArtifactInfo[] {
  if (!items) return []
  return groupArtifacts(items.filter(a => a.view === 'diff' && !a.missing))
    .map(g => g.latest)
    .sort((a, b) => Date.parse(b.created_at ?? '') - Date.parse(a.created_at ?? '') || 0)
}

export interface PlanList { kind: 'Checklist' | 'Plan'; done: number; total: number; next: string | null; doing?: string[] }

/** The checklist, else the engine's plan, as done / total and the next item to do. */
export function planList(checklist: { items: ChecklistItem[] } | undefined | null, plan: PlanLive | undefined | null): PlanList | null {
  if (checklist) {
    const items = checklist.items
    return { kind: 'Checklist', done: items.filter(i => i.done).length, total: items.length, next: items.find(i => !i.done)?.text ?? null, doing: items.filter(i => !i.done && i.in_progress).map(i => i.text) }
  }
  if (plan) {
    const items = plan.items
    const now = items.find(i => i.status === 'in_progress') ?? items.find(i => i.status !== 'completed')
    return {
      kind: 'Plan',
      done: items.filter(i => i.status === 'completed').length,
      total: items.length,
      next: now ? (now.status === 'in_progress' && now.active ? now.active : now.text) : null,
    }
  }
  return null
}

/** What the Plan tab's label carries: "18/23" with a list, else a count of tickets and diffs; null: no tab. */
export function planTabLabel(list: PlanList | null, tickets: number, diffs: number): string | null {
  if (list && list.total > 0) return `${list.done}/${list.total}`
  const n = tickets + diffs
  return n > 0 ? String(n) : null
}

/** Whether the session has anything for the Plan tab. */
export function hasPlanTab(list: PlanList | null, tickets: number, diffs: number): boolean {
  return list !== null || tickets > 0 || diffs > 0
}

/** "Checklist · 18 of 23 · Next: <item>", or "Checklist · all 23 done". */
export function planSummary(list: PlanList): string {
  if (list.total > 0 && list.done >= list.total) return `${list.kind} · all ${list.total} done`
  const head = `${list.kind} · ${list.done} of ${list.total}`
  return list.doing?.length ? `${head} · Doing: ${list.doing.join('; ')}` : list.next ? `${head} · Next: ${list.next}` : head
}

/** An item's commits as the Plan tab shows them: one chip, once per distinct commit set (the first
 *  row that holds it), with how many items it covers. */
export interface PlanChip { commits: string[]; diff?: string; covers: number }
export interface PlanRow { item: ChecklistItem; index: number; chip: PlanChip | null }

/** The checklist as the Plan tab lays it out: the open and doing items first (in their order), then
 *  the done ones. Items that share their commits (the same diff, else the same shas) show the chip
 *  once, on the first of them in that order. */
export function planRows(items: readonly ChecklistItem[]): { open: PlanRow[]; done: PlanRow[] } {
  const ordered = [...items.map((item, index) => ({ item, index }))].sort((a, b) => Number(a.item.done) - Number(b.item.done) || a.index - b.index)
  const keyOf = (it: ChecklistItem) => it.diff ? `diff:${it.diff}` : it.commits?.length ? `sha:${it.commits.join(',')}` : null
  const covers = new Map<string, number>()
  for (const { item } of ordered) {
    const k = keyOf(item)
    if (k) covers.set(k, (covers.get(k) ?? 0) + 1)
  }
  const seen = new Set<string>()
  const rows = ordered.map(({ item, index }): PlanRow => {
    const k = keyOf(item)
    if (!k || seen.has(k)) return { item, index, chip: null }
    seen.add(k)
    return { item, index, chip: { commits: item.commits ?? [], ...(item.diff ? { diff: item.diff } : {}), covers: covers.get(k) ?? 1 } }
  })
  return { open: rows.filter(r => !r.item.done), done: rows.filter(r => r.item.done) }
}

/** Who an item is assigned to, by name, never by id: the name the server recorded, else the running
 *  subagent's or started session's title, else what kind of worker it is. */
export function assigneeName(
  a: { kind: string; id: string },
  titles: Readonly<Record<string, string>> | undefined,
  subagents: readonly { key: string; childId?: string; title: string }[],
  children: readonly { id: string; title: string }[],
): string {
  const recorded = titles?.[`${a.kind}:${a.id}`]
  if (recorded) return recorded
  const live = a.kind === 'subagent'
    ? subagents.find(s => (s.childId || s.key) === a.id)?.title
    : children.find(c => c.id === a.id)?.title
  // The chat's placeholder for a subagent it never saw start says nothing more than the kind.
  if (live && !live.startsWith('Subagent · ')) return live
  return a.kind === 'subagent' ? 'a subagent' : 'a started session'
}
