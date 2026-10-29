import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react'
import type { SessionTabs } from '../hooks/useSessionTabs'
import { EXPLORER_CHAT_MIN } from '../hooks/useExplorerLayout'
import { groups, stripInsertion, type DropEdge, type TabGroup, type TabLayout, type TabSplit } from '../lib/sessionGroups'
import { moveSessionView, rememberViewScroll } from '../lib/moveSessionView'
import './SessionGroups.css'

export const sessionTabDragType = 'application/x-blerg-session-tab'
type Box = { left: number; top: number; width: number; height: number }
type Divider = { node: TabSplit; box: Box; parent: Box; stacked: boolean }
/** Flatten only DOM placement: stable keyed groups survive nesting, while the tree owns geometry. */
function geometry(tree: TabLayout, box: Box, leaves: { group: TabGroup; box: Box }[], dividers: Divider[]) {
  if (tree.type === 'group') { leaves.push({ group: tree, box }); return }
  const stacked = tree.axis === 'horizontal' || box.width < EXPLORER_CHAT_MIN * 2 + 6
  const size = Math.max(0, (stacked ? box.height : box.width) - 6)
  const minimum = stacked ? Math.min(160, size / 2) : EXPLORER_CHAT_MIN
  const first = Math.max(minimum, Math.min(size - minimum, size * tree.ratio))
  const a = { ...box, ...(stacked ? { height: first } : { width: first }) }
  const divider = { ...box, ...(stacked ? { top: box.top + first, height: 6 } : { left: box.left + first, width: 6 }) }
  const b = { ...box, ...(stacked ? { top: divider.top + 6, height: size - first } : { left: divider.left + 6, width: size - first }) }
  dividers.push({ node: tree, box: divider, parent: box, stacked })
  geometry(tree.first, a, leaves, dividers); geometry(tree.second, b, leaves, dividers)
}
function dropEdge(event: { clientX: number; clientY: number }, element: HTMLElement): DropEdge {
  const rect = element.getBoundingClientRect(), x = (event.clientX - rect.left) / rect.width, y = (event.clientY - rect.top) / rect.height
  const nearest = Math.min(x, 1 - x, y, 1 - y)
  if (nearest > .25) return 'center'
  return nearest === x ? 'left' : nearest === 1 - x ? 'right' : nearest === y ? 'top' : 'bottom'
}
const zoneOf = (edge: DropEdge, width: number): DropZone => width < EXPLORER_CHAT_MIN * 2 + 6 && (edge === 'left' || edge === 'right') ? edge === 'left' ? 'top' : 'bottom' : edge
function TabMounts({ tabs, group }: { tabs: SessionTabs; group: TabGroup }) {
  const ref = useRef<HTMLDivElement>(null)
  const { targetFor } = tabs
  useLayoutEffect(() => {
    const parent = ref.current!
    for (const id of group.tabs) {
      const target = targetFor(id)
      target.classList.toggle('session-file-view', id !== 'chat')
      target.hidden = id !== group.active
      moveSessionView(parent, target)
      target.setAttribute('role', 'tabpanel')
      target.setAttribute('aria-labelledby', id === 'chat' ? 'session-tab-chat' : `session-tab-${encodeURIComponent(id)}`)
      target.id = id === 'chat' ? 'session-view-chat' : `session-view-${encodeURIComponent(id)}`
    }
    for (const target of [...parent.children]) if (!group.tabs.includes((target as HTMLElement).dataset.viewId!)) {
      // A target owned by another group must stay connected until its atomic move.
      if (!tabs.groups.some(g => g.tabs.includes((target as HTMLElement).dataset.viewId!))) target.remove()
    }
  }, [group, targetFor, tabs.groups])
  useLayoutEffect(() => {
    const parent = ref.current!
    return () => { for (const target of [...parent.children]) rememberViewScroll(target as HTMLElement) }
  }, [])
  return <div className="session-group-content session-file-target" ref={ref} />
}
/** The drop overlay's region in a group: the half a split would give the tab, or the whole group for a move. */
export type DropZone = DropEdge
export type StripEnds = { start: boolean; end: boolean }
export default function SessionGroups({ tabs, bar }: { tabs: SessionTabs; bar(group: TabGroup, ends: StripEnds): ReactNode }) {
  const root = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })
  const [dragging, setDragging] = useState('')
  const [over, setOver] = useState<{ group: string; edge: DropEdge } | null>(null)
  const dragScope = tabs.scope
  const { phone, groups: tabGroups, focusGroup, drop: moveTabToGroup, move: reorder } = tabs
  useLayoutEffect(() => {
    const element = root.current!
    // Keep the previous object when nothing changed, so a resize notification does not re-render the groups.
    const measure = () => setSize(previous => previous.width === element.clientWidth && previous.height === element.clientHeight ? previous : { width: element.clientWidth, height: element.clientHeight })
    const observer = new ResizeObserver(measure); observer.observe(element); measure()
    return () => observer.disconnect()
  }, [])
  useEffect(() => {
    const start = (event: DragEvent) => {
      try {
        const data = JSON.parse(event.dataTransfer?.getData(sessionTabDragType) ?? '')
        if (data.scope === dragScope) setDragging(data.id)
      } catch { /* File and external drags are not tab drags. */ }
    }
    const clear = () => { setDragging(''); setOver(null) }
    const escape = (event: KeyboardEvent) => { if (event.key === 'Escape' && root.current?.hasAttribute('data-tab-dragging')) { event.preventDefault(); event.stopPropagation(); clear() } }
    document.addEventListener('dragstart', start); document.addEventListener('dragend', clear); document.addEventListener('drop', clear)
    document.addEventListener('keydown', escape); window.addEventListener('blur', clear)
    return () => { document.removeEventListener('dragstart', start); document.removeEventListener('dragend', clear); document.removeEventListener('drop', clear); document.removeEventListener('keydown', escape); window.removeEventListener('blur', clear) }
  }, [dragScope])
  // Portal events follow their React owner. Native listeners follow the actual group in the DOM.
  useEffect(() => {
    const element = root.current!
    const owner = (event: Event) => event.target instanceof Element ? event.target.closest<HTMLElement>('[data-group-id]') : null
    const focus = (event: Event) => {
      if (event.target instanceof Element && event.target.closest('.session-tabbar') && !event.target.closest('.session-tab')) return
      const group = owner(event)?.dataset.groupId
      if (!phone && group) focusGroup(group)
    }
    const dragOver = (event: DragEvent) => {
      const group = owner(event)
      if (!dragging || phone || tabs.tabFocused || !group) return
      event.preventDefault(); if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
      // Over a tab strip the strip draws its own insertion marker; the group gets no overlay.
      const next = event.target instanceof Element && event.target.closest('.session-tabbar') ? null : { group: group.dataset.groupId!, edge: dropEdge(event, group) }
      setOver(previous => previous?.group === next?.group && previous?.edge === next?.edge ? previous : next)
    }
    const drop = (event: DragEvent) => {
      const group = owner(event)
      if (!dragging || phone || tabs.tabFocused || !group) return
      event.preventDefault(); event.stopPropagation()
      const strip = event.target instanceof Element ? event.target.closest('.session-tabbar')?.querySelector('[role=tablist]') : null
      const before = strip ? stripInsertion(event.clientX, strip).before : undefined
      const targetGroup = tabGroups.find(g => g.id === group.dataset.groupId)!
      if (strip && targetGroup.tabs.includes(dragging)) { if (dragging !== 'chat') reorder(dragging, before!) }
      else moveTabToGroup(dragging, targetGroup.id, strip ? 'center' : dropEdge(event, group), strip ? before || undefined : undefined)
      setDragging(''); setOver(null)
    }
    element.addEventListener('focusin', focus); element.addEventListener('pointerdown', focus)
    element.addEventListener('dragover', dragOver); element.addEventListener('drop', drop)
    return () => { element.removeEventListener('focusin', focus); element.removeEventListener('pointerdown', focus); element.removeEventListener('dragover', dragOver); element.removeEventListener('drop', drop) }
  }, [phone, tabGroups, focusGroup, moveTabToGroup, reorder, dragging, tabs.tabFocused])
  const leaves: { group: TabGroup; box: Box }[] = [], dividers: Divider[] = []
  const tree: TabLayout = tabs.phone ? { type: 'group', id: 'phone', tabs: ['chat', ...tabs.tabs.map(t => t.id)], active: tabs.active } : tabs.layout
  geometry(tree, { left: 0, top: 0, ...size }, leaves, dividers)
  const multiple = !tabs.phone && groups(tree).length > 1
  // The workspace toggles ride on the top row: the explorer at the left end of the top-left strip,
  // shell, board and panel at the right end of the top-right strip (the first leaf is always top-left).
  const topRight = leaves.reduce((best, leaf) => leaf.box.top === 0 && leaf.box.left >= best.box.left ? leaf : best, leaves[0])
  // Announce a change of focused group (or of the number of groups) only: opening or closing a
  // tab in the focused group stays quiet.
  const [announcement, setAnnouncement] = useState({ focused: '', groups: 0, text: '' })
  if (announcement.focused !== tabs.focused || announcement.groups !== leaves.length) {
    const index = leaves.findIndex(({ group }) => group.id === tabs.focused), count = leaves[index]?.group.tabs.length ?? 0
    setAnnouncement({ focused: tabs.focused, groups: leaves.length, text: `Focused tab group ${Math.max(0, index) + 1} of ${leaves.length}${multiple ? `, ${count} ${count === 1 ? 'tab' : 'tabs'}` : ''}` })
  }
  return <div ref={root} className="session-groups" data-testid="session-groups" data-tab-dragging={dragging || undefined} onDragLeave={event => { if (!event.currentTarget.contains(event.relatedTarget as Node)) setOver(null) }}>
    {leaves.map(({ group, box }, index) => <section key={group.id} className={`session-group${tabs.focused === group.id ? ' focused' : ''}`} id={`session-group-${encodeURIComponent(group.id)}`} data-group-id={group.id} aria-label={`Tab group ${index + 1}`} hidden={!tabs.phone && tabs.tabFocused && group.id !== tabs.focused} style={(tabs.tabFocused && group.id === tabs.focused ? { left: 0, top: 0, ...size } : box) as CSSProperties}
      >
      {multiple && bar(group, { start: tabs.tabFocused ? group.id === tabs.focused : index === 0, end: tabs.tabFocused ? group.id === tabs.focused : group.id === topRight.group.id })}
      <TabMounts tabs={tabs} group={group} />
      {/* As VS Code: one translucent rectangle where the tab will land, nothing else. A group too
          narrow to split side by side stacks, so its left/right halves are drawn as top/bottom. */}
      {dragging && over?.group === group.id && <div data-testid="drop-overlay" data-zone={zoneOf(over.edge, box.width)} className={`session-drop-overlay${multiple ? ' with-tabbar' : ''}`} aria-hidden="true" />}
    </section>)}
    <span className="session-groups-announce" role="status" aria-live="polite" data-testid="group-announce">{announcement.text}</span>
    {!tabs.tabFocused && dividers.map(({ node, box, parent, stacked }, index) => {
      const extent = Math.max(1, (stacked ? parent.height : parent.width) - 6)
      const minimum = stacked ? Math.min(160, extent / 2) : EXPLORER_CHAT_MIN
      const ratio = ((stacked ? box.top - parent.top : box.left - parent.left) / extent) * 100
      return <div key={node.id} role="separator" tabIndex={0} aria-label={`Resize tab groups ${index + 1}`} aria-orientation={stacked ? 'horizontal' : 'vertical'} aria-controls={[...groups(node.first), ...groups(node.second)].map(g => `session-group-${encodeURIComponent(g.id)}`).join(' ')} aria-valuemin={Math.round(Math.max(15, minimum / extent * 100))} aria-valuemax={Math.round(Math.min(85, (1 - minimum / extent) * 100))} aria-valuenow={Math.round(ratio)} className={`session-group-divider${stacked ? ' stacked' : ''}`} style={box as CSSProperties}
      onDoubleClick={() => tabs.resize(node.id, .5)} onKeyDown={event => {
        const decrease = stacked ? 'ArrowUp' : 'ArrowLeft', increase = stacked ? 'ArrowDown' : 'ArrowRight'
        if (![decrease, increase, 'Home', 'End'].includes(event.key)) return
        event.preventDefault(); tabs.resize(node.id, event.key === 'Home' ? .15 : event.key === 'End' ? .85 : node.ratio + (event.key === decrease ? -.05 : .05))
      }} onPointerDown={event => { if (event.button !== 0) return; event.preventDefault(); event.currentTarget.focus({ preventScroll: true }); event.currentTarget.setPointerCapture(event.pointerId) }}
      onPointerMove={event => {
        if (!event.currentTarget.hasPointerCapture(event.pointerId)) return
        const rect = root.current!.getBoundingClientRect()
        const offset = stacked ? event.clientY - rect.top - parent.top : event.clientX - rect.left - parent.left
        tabs.resize(node.id, offset / Math.max(1, (stacked ? parent.height : parent.width) - 6), true)
      }} onPointerUp={event => { if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId) }} />})}
  </div>
}
