import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import './ProposalEditSheet.css'

export type PlanProposal = { id: string; actor?: {id:string;kind:string}; milestone_id: string; revision: number; status: string; changes: { id: string; operation: string; item: { title?: string; board_id: string; card_number: number; reason: string; destination?: string } }[] }
export default function BlackboardProposal({ proposal, milestoneName, title, onClose }: { proposal: PlanProposal; milestoneName: string; title: (board: string, number: number) => string | undefined; onClose: () => void }) {
 const dialog = useRef<HTMLDivElement>(null)
 useEffect(() => { const previous = document.activeElement as HTMLElement | null; dialog.current?.focus(); return () => { if (previous?.isConnected) previous.focus() } }, [])
 return createPortal(<div className="artifact-overlay"><div ref={dialog} className="artifact-dialog ticket-modal" role="dialog" aria-modal="true" aria-label={`Plan changes for ${milestoneName}`} tabIndex={-1} onKeyDown={e => { e.stopPropagation(); if (e.key === 'Escape') onClose(); if (e.key === 'Tab') { e.preventDefault(); dialog.current?.querySelector<HTMLButtonElement>('button')?.focus() } }}>
  <header className="artifact-header"><div className="artifact-header-text"><h2 className="ticket-modal-title">Plan changes · {milestoneName}</h2></div><div className="ticket-modal-actions"><button className="artifact-close" aria-label="Close plan review" title="Close plan review" onClick={onClose}>×</button></div></header>
  <div className="artifact-body ticket-modal-body"><p>Revision {proposal.revision}. Discuss changes with Main. Decisions happen by replying to Main’s question in chat or Needs you.</p><ul>{proposal.changes.map(c => <li key={c.id}>{c.operation === 'link' ? 'Link board' : c.operation === 'unlink' ? 'Unlink board' : c.operation === 'add' ? 'Add' : c.operation === 'remove' ? 'Remove' : 'Move'} · {c.operation === 'link' || c.operation === 'unlink' ? c.item.board_id : title(c.item.board_id,c.item.card_number) || c.item.title || `Card #${c.item.card_number}`}<br />{c.item.reason || 'No reason recorded'}{c.item.destination ? ` → ${c.item.destination}` : ''}</li>)}</ul></div>
 </div></div>, document.body)
}
