import { useState } from 'react'
import type { CategoryAction } from '../hooks/useChannelCategories'
export default function CategoryEditor({ editing, action, close, busy }: { editing: { id?: string; name: string }; action(request: CategoryAction): Promise<boolean>; close(): void; busy: boolean }) {
  const [name, setName] = useState(editing.name)
  return <form className="dock-category-editor" onSubmit={event => { event.preventDefault(); void action({ action: editing.id ? 'rename' : 'create', id: editing.id, name }).then(ok => { if (ok) close() }) }}>
    <input autoFocus aria-label="Category name" maxLength={80} value={name} onChange={event => setName(event.target.value)} onKeyDown={event => { if (event.key === 'Escape') { event.stopPropagation(); close() } }} />
    <button className="btn small" disabled={busy || !name.trim()}>Save category</button><button type="button" className="btn small" onClick={close}>Cancel</button>
  </form>
}
