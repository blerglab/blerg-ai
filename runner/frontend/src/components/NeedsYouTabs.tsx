import { useState } from 'react'
import { createPortal } from 'react-dom'
import type { SessionTabs } from '../hooks/useSessionTabs'
import type { useQuestionItems, AttentionItem } from '../hooks/useQuestionItems'
import { AttentionRows } from './QuestionQueue'
import RuntimeIcon from './RuntimeIcon'
import InitialLoader from './InitialLoader'
import { newestAttention } from '../lib/needsYou'
import './NeedsYouTabs.css'

type Attention = ReturnType<typeof useQuestionItems>
export function NeedsYou({ attention, settled, active = true }: { attention: Attention; settled: boolean; active?: boolean }) {
  const [worker, setWorker] = useState('')
  const sorted = newestAttention(attention.items)
  const owners = [...new Map(sorted.map(item => [item.session.id, item.session])).values()]
  const selected = owners.some(owner => owner.id === worker) ? worker : ''
  const shown = selected ? sorted.filter(item => item.session.id === selected) : sorted
  const grouped = new Map<string, AttentionItem[]>()
  for (const item of shown) {
    const group = grouped.get(item.session.id)
    if (group) group.push(item)
    else grouped.set(item.session.id, [item])
  }
  return <section className="needs-you-tab" aria-label="Needs you" data-view-focus tabIndex={-1}>
    {!settled && sorted.length === 0 && <p className="rail-empty">Gathering what needs your attention.</p>}
    <InitialLoader loading={!settled} hasContent={sorted.length > 0} label="Needs you">
      {sorted.length > 0 && <div className="needs-you-toolbar"><label>Worker <select aria-label="Worker" value={selected} onChange={event => setWorker(event.target.value)}><option value="">All workers and channels</option>{owners.map(owner => <option key={owner.id} value={owner.id}>{owner.id === attention.root ? 'Main' : owner.title || 'Untitled chat'}</option>)}</select></label><span className="rail-item-line" role="status">{selected ? `${shown.length} of ${sorted.length}` : sorted.length} waiting</span></div>}
      {!sorted.length && <p className="rail-empty">Nothing needs you. You're all caught up.</p>}
      <div className="needs-you-groups">{[...grouped].map(([id, items]) => {
        const name = id === attention.root ? 'Main' : items[0].session.title || 'Untitled chat'
        return <section className="needs-you-group" aria-label={name} key={id}><header className="needs-you-group-header"><RuntimeIcon runtime={items[0].session.runtime} /><h3>{name}</h3><span className="rail-count" aria-label={`${items.length} waiting ${items.length === 1 ? 'item' : 'items'}`}>{items.length}</span></header>{items.map(item => <AttentionRows key={item.id} root={attention.root} items={[item]} inlineAnswers active={active} />)}</section>
      })}</div>
    </InitialLoader>
  </section>
}
export default function NeedsYouTabs({ tabs, attention, settled }: { tabs: SessionTabs; attention: Attention; settled: boolean }) {
  return tabs.tabs.map(tab => tab.view.kind === 'needs-you' && createPortal(<NeedsYou attention={attention} settled={settled} active={tabs.visible.includes(tab.id)} />, tabs.targetFor(tab.id), tab.id))
}
