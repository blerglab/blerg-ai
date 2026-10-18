import { useState } from 'react'
import { apiFetch } from '../apiFetch'
import { useSessionBoards } from '../lib/sessionBoards'
import type { ProposalInfo } from '../types'

const text = (v: unknown) => typeof v === 'string' ? v : ''
const control = { display: 'block', width: '100%', color: 'var(--chalk)', background: 'var(--basalt)', border: '1px solid var(--stone)', borderRadius: 4, padding: 6, boxSizing: 'border-box' as const }

/** Editing changes only the pending draft. The separate confirmation performs the write. */
export default function BoardFileDraft({ proposal, onSaved, onCancel }: { proposal: ProposalInfo; onSaved: (p: ProposalInfo) => void; onCancel: () => void }) {
  const boards = useSessionBoards(true)
  const [expected] = useState(proposal.arguments_raw)
  const [board, setBoard] = useState(text(proposal.arguments.board_id))
  const [title, setTitle] = useState(text(proposal.arguments.title))
  const [body, setBody] = useState(text(proposal.arguments.body))
  const [type, setType] = useState(text(proposal.arguments.type))
  const [sensitive, setSensitive] = useState(proposal.arguments.sensitive === true)
  const [repos, setRepos] = useState(() => Array.isArray(proposal.arguments.repos) ? proposal.arguments.repos.filter(r => typeof r === 'string').join(', ') : '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const verified = boards.state === 'ready' && boards.configured && boards.boards.some(b => b.id === board)
  async function save() {
    if (busy || !verified || !expected || !title.trim()) return
    setBusy(true); setError('')
    try {
      const response = await apiFetch(`/api/proposals/${encodeURIComponent(proposal.id)}/draft`, {
        method: 'PATCH', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ expected_arguments_raw: expected, draft: { board_id: board, title, body, type, sensitive, repos: [...new Set(repos.split(',').map(r => r.trim()).filter(Boolean))] } }),
      })
      if (!response.ok) {
        const failure = await response.json().catch(() => ({}))
        throw new Error(typeof failure.error === 'string' ? failure.error : `HTTP ${response.status}`)
      }
      const updated = await response.json() as ProposalInfo
      if (updated.id !== proposal.id || updated.kind !== 'board_file' || updated.state !== 'pending' || typeof updated.arguments_raw !== 'string') throw new Error('The saved draft could not be verified. Reload proposals before approving.')
      onSaved(updated)
    } catch (e) { setError(e instanceof Error ? e.message : 'Could not save the draft.') }
    finally { setBusy(false) }
  }
  return <div data-testid="board-file-draft" style={{ display: 'grid', gap: 8 }}>
    <p>Save your edits, then review and approve the updated draft. Saving files nothing.</p>
    <fieldset disabled={busy} style={{ border: 0, padding: 0, display: 'grid', gap: 8 }}>
      <label>Target board<select value={board} onChange={e => setBoard(e.target.value)} style={control}>
        <option value="">Choose a board</option>
        {board && !boards.boards.some(b => b.id === board) && <option value={board}>Previously selected board (unverified)</option>}
        {boards.state === 'ready' && boards.configured && boards.boards.map(b => <option key={b.id} value={b.id}>{b.name}</option>)}
      </select></label>
      {!verified && <div role="status">{boards.state === 'loading' ? 'Checking board access…' : 'Choose an available board before saving.'}{' '}{boards.state === 'failed' && <button type="button" onClick={boards.retry}>Retry boards</button>}</div>}
      <label>Ticket title<input value={title} maxLength={200} onChange={e => setTitle(e.target.value)} style={control} /></label>
      <label>Reproduction and details<textarea value={body} rows={8} maxLength={65536} onChange={e => setBody(e.target.value)} style={control} /></label>
      <label>Ticket type<input value={type} maxLength={64} onChange={e => setType(e.target.value)} style={control} /></label>
      <label>Target repositories (optional)<input value={repos} onChange={e => setRepos(e.target.value)} style={control} /></label>
      <small>Comma-separated repositories on the target board. The originating repository stays unchanged in the ticket's origin.</small>
      <label><input type="checkbox" checked={sensitive} onChange={e => setSensitive(e.target.checked)} /> Restrict reproduction details to board admins</label>
      <div><button type="button" disabled={!verified || !expected || !title.trim()} onClick={() => void save()}>{busy ? 'Saving…' : 'Save draft'}</button>{' '}<button type="button" onClick={onCancel}>Cancel editing</button></div>
    </fieldset>
    {error && <div role="alert">{error}</div>}
  </div>
}
