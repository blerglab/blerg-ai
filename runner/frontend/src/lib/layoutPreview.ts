import { tabLabel, type SessionTab } from '../hooks/useSessionTabs'
import { groups, normalizeLayout } from './sessionGroups'
import type { WorkspaceLayout } from './workspaceLayouts'

export const mainOnlyLayout: WorkspaceLayout = { tabs: [], active: 'chat', focused: 'main', layout: { type: 'group', id: 'main', tabs: ['chat'], active: 'chat' }, panels: {} }
export function previewTree(layout: WorkspaceLayout) {
  const tabs = Array.isArray(layout.tabs) ? layout.tabs.filter(tab => tab?.id && tab.view) : []
  return normalizeLayout(layout.layout, [...(layout.mainClosed ? [] : ['chat']), ...tabs.map(tab => tab.id)], layout.active)
}
export function previewLabel(tab?: SessionTab) {
  if (!tab) return 'Tabs'
  try { return tabLabel(tab.view) }
  catch { console.warn('Layout preview label unavailable: invalid saved metadata'); return 'Tab' }
}
export function layoutSummary(layout: WorkspaceLayout) {
  const names = groups(previewTree(layout)).flatMap(group => group.tabs).map(id => id === 'chat' ? 'Main' : previewLabel(layout.tabs?.find(tab => tab?.id === id)))
  return [...new Set(names), ...(layout.panels?.explorer?.open ? ['explorer open'] : []), ...(layout.panels?.['session-panel']?.open ? ['panel open'] : [])].join(' · ') || 'No tabs open'
}
