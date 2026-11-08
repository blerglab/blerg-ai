import type { ButtonHTMLAttributes, HTMLAttributes, ReactNode } from 'react'
import './board.css'

export interface BoardCardData {
  number: number; reference?: string; title: string; type?: string; priority?: string
  size?: string | null; risk?: string | null; complexity?: string | null
  tags?: string[] | null; stale_at?: string | null; stuck_at?: string | null
}

export function ageShort(iso: string): string {
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000)
  if (secs < 90) return `${Math.round(secs)}s`
  if (secs < 5400) return `${Math.round(secs / 60)}m`
  if (secs < 129600) return `${(secs / 3600).toFixed(1)}h`
  return `${Math.round(secs / 86400)}d`
}

// Hosts own data, drag state, navigation and any service-specific indicators.
export function BoardColumn({ name, count, terminal, headingId, children, focused, onFocusColumn, className = '', ...props }: HTMLAttributes<HTMLElement> & {
  name: string; count: ReactNode; terminal?: boolean; headingId?: string; focused?: boolean; onFocusColumn?: () => void
}) {
  // button-guard-ignore: host ARIA/drag props precede the mandatory shared-board-column class, styled in board.css.
  return <section {...props} className={`column shared-board-column${terminal ? ' terminal' : ''} ${className}`}>
    <header onDoubleClick={onFocusColumn}><span className="blaze" /><h3 id={headingId}>{name}</h3><span className="count">{count}</span>{onFocusColumn && <ColumnFocusButton name={name} focused={focused} onClick={onFocusColumn} />}</header>
    {children}
  </section>
}

export function BoardCard({ card, head, children, className = '', ...props }: ButtonHTMLAttributes<HTMLButtonElement> & {
  card: BoardCardData; head?: ReactNode
}) {
  // button-guard-ignore: forwarded host handlers precede the mandatory shared-board-card class, styled in board.css.
  return <button {...props} type="button" className={`card shared-board-card ${className}`}>
    <div className="head"><span className="num">{card.reference || `#${card.number}`}</span><span className="title">{card.title}</span>{head}</div>
    <div className="meta">
      {card.type && <span className="chip type">{card.type}</span>}
      {(card.priority === 'high' || card.priority === 'urgent') && <span className={`chip priority-${card.priority}`}>{card.priority}</span>}
      {card.size && <span className="chip">{card.size}</span>}
      {card.risk && <span className="chip">Risk: {card.risk}</span>}
      {card.complexity && <span className="chip">Complexity: {card.complexity}</span>}
      {children}
      {card.stuck_at && <span className="chip stuck" title="blerg-board stopped working this card and wants a human — open it, the newest comment says why and what to do; Run clears the flag and retries">⏸ stuck {ageShort(card.stuck_at)}</span>}
      {card.stale_at && <span className="chip stale" title="claimed but idle — no active workspace and no card activity for a while; update it or release it back to ready">💤 stale {ageShort(card.stale_at)}</span>}
      {(card.tags ?? []).slice(0, 3).map(t => <span className="chip" key={t}>{t}</span>)}
    </div>
  </button>
}

export function ColumnFocusButton({ name, focused, onClick }: { name: string; focused?: boolean; onClick: () => void }) {
 return <button type="button" className="active-clear" title={focused ? 'Back to all columns (Esc)' : `Focus ${name}`} aria-label={focused ? 'Back to all columns' : `Focus ${name}`} onClick={onClick} onDoubleClick={e => e.stopPropagation()}>{focused ? 'All columns' : <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true"><path d="M9 3H3v6m12-6h6v6M3 15v6h6m6 0h6v-6" /></svg>}</button>
}
