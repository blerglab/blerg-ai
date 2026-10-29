/** View-agnostic tree: future Problems/Tests tabs only need a registered tab identity. */
export type TabGroup = { type: 'group'; id: string; tabs: string[]; active: string }
export type TabSplit = { type: 'split'; id: string; axis: 'vertical' | 'horizontal'; ratio: number; first: TabLayout; second: TabLayout }
export type TabLayout = TabGroup | TabSplit
export type DropEdge = 'center' | 'left' | 'right' | 'top' | 'bottom'
export const groups = (tree: TabLayout): TabGroup[] => tree.type === 'group' ? [tree] : [...groups(tree.first), ...groups(tree.second)]
export function mapGroups(tree: TabLayout, fn: (group: TabGroup) => TabGroup): TabLayout {
  if (tree.type === 'group') return fn(tree)
  const first = mapGroups(tree.first, fn), second = mapGroups(tree.second, fn)
  return { ...tree, first, second }
}
function prune(tree: TabLayout): TabLayout | null {
  if (tree.type === 'group') return tree.tabs.length ? tree : null
  const first = prune(tree.first), second = prune(tree.second)
  return first && second ? { ...tree, first, second } : first ?? second
}
/** Bounded validation handles old preferences and untrusted browser storage without recursion bombs. */
export function normalizeLayout(value: unknown, ids: string[], active: string): TabLayout {
  const seenTabs = new Set<string>(), seenNodes = new Set<string>()
  let count = 0
  const read = (value: unknown, depth: number): TabLayout | null => {
    if (!value || typeof value !== 'object' || depth > 24 || ++count > 256) return null
    const node = value as { type?: string; id?: string; tabs?: unknown; active?: string; first?: unknown; second?: unknown; axis?: string; ratio?: number }
    let id = typeof node.id === 'string' ? node.id : `restored-${count}`
    while (seenNodes.has(id)) id = `restored-${++count}`
    seenNodes.add(id)
    if (node.type === 'group' && Array.isArray(node.tabs)) {
      const tabs = node.tabs.filter((tab): tab is string => typeof tab === 'string' && ids.includes(tab) && !seenTabs.has(tab) && !!seenTabs.add(tab))
      return tabs.length ? { type: 'group', id, tabs, active: tabs.includes(node.active ?? '') ? node.active! : tabs[0] } : null
    }
    if (node.type !== 'split') return null
    const first = read(node.first, depth + 1), second = read(node.second, depth + 1)
    return first && second ? { type: 'split', id, axis: node.axis === 'horizontal' ? 'horizontal' : 'vertical', ratio: clampRatio(node.ratio ?? .5), first, second } : first ?? second
  }
  let tree = read(value, 0)
  const missing = ids.filter(id => !seenTabs.has(id))
  if (!tree) return { type: 'group', id: 'main', tabs: ids, active: ids.includes(active) ? active : ids[0] }
  if (missing.length) {
    const first = groups(tree)[0].id
    tree = mapGroups(tree, group => group.id === first ? { ...group, tabs: [...group.tabs, ...missing] } : group)
  }
  return mapGroups(tree, group => group.tabs.includes('chat') ? { ...group, tabs: ['chat', ...group.tabs.filter(id => id !== 'chat')] } : group)
}
export function activateTab(tree: TabLayout, id: string): TabLayout {
  return mapGroups(tree, group => group.tabs.includes(id) ? { ...group, active: id } : group)
}
export function removeTab(tree: TabLayout, id: string): TabLayout {
  return prune(mapGroups(tree, group => {
    const index = group.tabs.indexOf(id)
    const tabs = group.tabs.filter(tab => tab !== id)
    return { ...group, tabs, active: group.active === id ? tabs[Math.max(0, index - 1)] ?? '' : group.active }
  })) ?? { type: 'group', id: 'main', tabs: ['chat'], active: 'chat' }
}
export function moveToGroup(tree: TabLayout, id: string, target: string, edge: DropEdge, newId: string, before?: string): TabLayout {
  const source = groups(tree).find(group => group.tabs.includes(id))
  if (!source || !groups(tree).some(group => group.id === target)) return tree
  if (source.id === target && (edge === 'center' || source.tabs.length === 1)) return tree
  const removed = mapGroups(tree, group => {
    const tabs = group.tabs.filter(tab => tab !== id), index = group.tabs.indexOf(id)
    return { ...group, tabs, active: group.active === id ? tabs[Math.max(0, index - 1)] ?? '' : group.active }
  })
  const insert = (node: TabLayout): TabLayout => {
    if (node.type === 'split') return { ...node, first: insert(node.first), second: insert(node.second) }
    if (node.id !== target) return node
    if (edge === 'center') {
      const tabs = [...node.tabs], index = tabs.indexOf(before ?? '')
      tabs.splice(id === 'chat' ? 0 : index < 0 ? tabs.length : Math.max(tabs[0] === 'chat' ? 1 : 0, index), 0, id)
      return { ...node, tabs, active: id }
    }
    const next: TabGroup = { type: 'group', id: newId, tabs: [id], active: id }
    const precedes = edge === 'left' || edge === 'top'
    return { type: 'split', id: `split-${newId}`, axis: edge === 'left' || edge === 'right' ? 'vertical' : 'horizontal', ratio: .5, first: precedes ? next : node, second: precedes ? node : next }
  }
  return prune(insert(removed)) ?? tree
}
export const clampRatio = (ratio: number) => Number.isFinite(ratio) ? Math.max(.15, Math.min(.85, ratio)) : .5
export function resizeSplit(tree: TabLayout, id: string, ratio: number): TabLayout {
  return tree.type === 'group' ? tree : { ...tree, ratio: tree.id === id ? clampRatio(ratio) : tree.ratio, first: resizeSplit(tree.first, id, ratio), second: resizeSplit(tree.second, id, ratio) }
}

/** Where a tab dropped at clientX lands in a strip: before the first file tab whose middle is right
 *  of the pointer (Chat stays pinned first), else at the end; `left` is the insertion marker's offset. */
export function stripInsertion(clientX: number, strip: Element): { before: string; left: number } {
  const tabs = [...strip.children].filter((node): node is HTMLElement => node instanceof HTMLElement && node.classList.contains('session-tab'))
  for (const tab of tabs) {
    const id = tab.querySelector<HTMLElement>('[data-tab-id]')?.dataset.tabId
    if (!id || id === 'chat') continue
    const rect = tab.getBoundingClientRect()
    if (clientX < rect.left + rect.width / 2) return { before: id, left: tab.offsetLeft }
  }
  const last = tabs.at(-1)
  return { before: '', left: last ? last.offsetLeft + last.offsetWidth : 0 }
}
