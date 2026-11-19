import type { ReactNode } from 'react'
import type { TabLayout } from '../lib/sessionGroups'
import { previewLabel, previewTree } from '../lib/layoutPreview'
import type { WorkspaceLayout } from '../lib/workspaceLayouts'

export default function LayoutPreview({ layout, name }: { layout: WorkspaceLayout; name: string }) {
  const viewport = Math.max(390, layout.viewport?.width ?? 1440)
  const explorer = layout.panels?.explorer?.open ? Math.min(90, Math.max(34, layout.panels.explorer.width / viewport * 400)) : 0
  const panel = layout.panels?.['session-panel']?.open ? Math.min(90, Math.max(34, layout.panels['session-panel'].width / viewport * 400)) : 0
  const tree = previewTree(layout)
  const draw = (node: TabLayout, x: number, y: number, width: number, height: number): ReactNode => {
    if (node.type === 'split') {
      const vertical = node.axis === 'vertical', first = (vertical ? width : height) * node.ratio
      return <g key={node.id}>{draw(node.first, x, y, vertical ? first - 3 : width, vertical ? height : first - 3)}{draw(node.second, vertical ? x + first + 3 : x, vertical ? y : y + first + 3, vertical ? width - first - 3 : width, vertical ? height : height - first - 3)}</g>
    }
    const tab = layout.tabs?.find(tab => tab?.id === node.active)
    const label = node.active === 'chat' ? 'Main' : previewLabel(tab)
    const characters = Math.max(1, Math.floor((width - 20) / 5))
    return <g key={node.id} data-pane="group"><rect className={node.active === layout.active ? 'layout-preview-active' : 'layout-preview-group'} x={x} y={y} width={Math.max(1, width)} height={Math.max(1, height)} rx="5" /><path className="layout-preview-rule" d={`M${x + 1} ${y + 22}h${Math.max(0, width - 2)}`} />{height >= 26 && width >= 40 && <text x={x + 10} y={y + 15}>{label.length > characters ? label.slice(0, Math.max(1, characters - 1)) + '…' : label}</text>}{[0, 1, 2].filter(line => height > 50 + line * 12 && width > 30).map(line => <path key={line} className="layout-preview-content" d={`M${x + 12} ${y + 42 + line * 12}h${Math.max(1, (width - 24) * (line === 2 ? .45 : .72))}`} />)}</g>
  }
  return <svg className="layout-preview" role="img" aria-label={`Pane preview for ${name}`} viewBox="0 0 420 220">
    <rect className="layout-preview-window" x="1" y="1" width="418" height="218" rx="8" />
    <path className="layout-preview-rule" d="M1 22h418" />
    {[12, 22, 32].map(x => <circle className="layout-preview-dot" key={x} cx={x} cy="12" r="2" />)}
    {explorer > 0 && <g data-pane="explorer"><rect className="layout-preview-side" x="10" y="32" width={explorer} height="178" rx="5" /><text x="17" y="48">{explorer >= 50 ? 'Explorer' : 'E'}</text>{[0, 1, 2, 3].map(line => <path key={line} className="layout-preview-content" d={`M18 ${66 + line * 20}h${Math.max(8, explorer - 18)}`} />)}</g>}
    {draw(tree, 10 + (explorer ? explorer + 6 : 0), 32, 400 - (explorer ? explorer + 6 : 0) - (panel ? panel + 6 : 0), 178)}
    {panel > 0 && <g data-pane="panel"><rect className="layout-preview-side" x={410 - panel} y="32" width={panel} height="178" rx="5" /><text x={417 - panel} y="48">{panel >= 50 ? 'Panel' : 'P'}</text>{[0, 1, 2].map(line => <rect className="layout-preview-group" key={line} x={417 - panel} y={60 + line * 44} width={Math.max(8, panel - 14)} height="32" rx="3" />)}</g>}
  </svg>
}
