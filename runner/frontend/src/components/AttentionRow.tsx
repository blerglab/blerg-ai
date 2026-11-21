import type { ComponentPropsWithRef, ReactNode } from 'react'
import ExplorerTreeRow from './ExplorerTreeRow'
import './AttentionRow.css'

/** Questions and file reviews share one flat row and one dismissal control. */
export default function AttentionRow({ action, ...props }: ComponentPropsWithRef<'button'> & { action?: ReactNode }) {
  return <div className="attention-row"><ExplorerTreeRow {...props} role="button" icon={null} onToggle={undefined} />{action}</div>
}

export function AttentionDismiss({ onDismiss, label = 'Dismiss' }: { onDismiss(): void; label?: string }) {
  return <button type="button" className="artifact-close" title="Close without answering" aria-label={label} onClick={onDismiss}>⊘</button>
}
