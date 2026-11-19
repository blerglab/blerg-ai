import type { ComponentPropsWithRef, ReactNode } from 'react'
import './WorkspaceTree.css'

/** Shared Explorer surface: sessions use the same row, chevron and trailing status slot. */
export default function ExplorerTreeRow({ children, icon, status, expanded, onToggle, ...props }: Omit<ComponentPropsWithRef<'button'>, 'className'> & { icon: ReactNode; status?: ReactNode; expanded?: boolean; onToggle?(): void }) {
  // button-guard-ignore: reusable row always supplies workspace-row, whose resting surface has a background and border
  return <button {...props} type="button" role={props.role ?? 'treeitem'} className="workspace-row" aria-expanded={expanded ?? props['aria-expanded']}>
    <span className="workspace-chevron" aria-hidden="true" onClick={onToggle ? event => { event.stopPropagation(); onToggle() } : undefined}>{expanded === undefined ? '' : expanded ? '▾' : '▸'}</span>
    {icon}{children}<span className="workspace-codes">{status}</span>
  </button>
}
