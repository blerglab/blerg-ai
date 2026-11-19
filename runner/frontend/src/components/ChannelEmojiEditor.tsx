import { useState } from 'react'
import { apiFetch } from '../apiFetch'
import { useSessionStore } from '../hooks/useSessionStore'
import type { SessionInfo } from '../types'

export default function ChannelEmojiEditor({ session, close }: { session: SessionInfo; close(): void }) {
 const [emoji, setEmoji] = useState(session.emoji ?? '')
 const [busy, setBusy] = useState(false)
 const [error, setError] = useState('')
 const save = async () => {
  setBusy(true); setError('')
  try {
   const response = await apiFetch(`/api/sessions/${encodeURIComponent(session.id)}`, { method: 'PATCH', headers: {'Content-Type': 'application/json'}, body: JSON.stringify({emoji}) })
   if (!response.ok) { const body = await response.json(); throw new Error(body.error || 'Could not save emoji.') }
   useSessionStore.setState(state => ({ sessions: state.sessions.map(s => s.id === session.id ? {...s, emoji} : s) }))
   close()
  } catch (err) { setError(err instanceof Error ? err.message : 'Could not save emoji.') }
  finally { setBusy(false) }
 }
 return <form className="dock-category-editor" aria-label="Channel emoji" onSubmit={event => { event.preventDefault(); void save() }} onKeyDown={event => { if (event.key === 'Escape') { event.stopPropagation(); close() } }}>
  <label>Channel emoji<input autoFocus aria-label="Emoji" value={emoji} onChange={event => setEmoji(event.target.value)} /></label>
  <span>Choose one emoji. Leave empty to use the default icon.</span>
  {error && <p role="alert">{error}</p>}
  <button className="btn small" disabled={busy}>Save emoji</button><button type="button" className="btn small" disabled={busy} onClick={close}>Cancel</button>
 </form>
}
