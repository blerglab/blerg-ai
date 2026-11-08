import type { Ref } from 'react'
import './DockHeading.css'

export default function DockHeading({ title, action, onAdd, buttonRef, disabled, reason }: { title: string; action: string; onAdd?(): void; buttonRef?: Ref<HTMLButtonElement>; disabled?: boolean; reason?: string }) {
  return <div className="dock-list-heading"><h3 title={title}>{title}</h3>{onAdd && <button type="button" ref={buttonRef} className="dock-list-add" title={reason || action} aria-label={action} disabled={disabled} onClick={onAdd}>+</button>}</div>
}
