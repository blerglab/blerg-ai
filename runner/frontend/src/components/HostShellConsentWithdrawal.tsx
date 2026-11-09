import { useCallback, useEffect, useState } from 'react'
import { apiFetch } from '../apiFetch'

type Consent = { id: string; account: string; folder: string; withdrawn?: boolean; cleanup_pending?: boolean }
/** Denial metadata remains available while the desktop cannot be reached. */
export default function HostShellConsentWithdrawal({ session }: { session: string }) {
  const [items, setItems] = useState<Consent[]>([])
  const [next, setNext] = useState('')
  const [busy, setBusy] = useState('')
  const [message, setMessage] = useState('')
  const load = useCallback((after = '', signal?: AbortSignal) => {
    return apiFetch(`/api/sessions/${encodeURIComponent(session)}/shell-consents${after ? `?after=${encodeURIComponent(after)}` : ''}`, { signal }).then(async response => {
      if (!response?.ok) return
      const page = await response.json() as { consents?: Consent[]; next?: string }
      if (signal?.aborted || !Array.isArray(page.consents) || page.consents.length > 32 || page.consents.some(c => !c || typeof c.id !== 'string' || typeof c.account !== 'string' || typeof c.folder !== 'string')) return
      setItems(page.consents); setNext(typeof page.next === 'string' ? page.next : '')
    }).catch(() => { /* Listing supplies no authority and never changes terminal state. */ })
  }, [session])
  useEffect(() => { const abort = new AbortController(); void load('', abort.signal); return () => abort.abort() }, [load])
  if (!items.length && !message) return null
  return <section className="shell-offline-consent" aria-label="Host shell consent">
    <p>You can withdraw host shell consent while the desktop is offline. Withdrawing consent ends that host shell.</p>
    {items.map(consent => <div className="shell-consent-row" key={consent.id}>
      <span>{consent.account} · {consent.folder}</span>
      {consent.withdrawn ? <span role="status">{consent.cleanup_pending ? 'Consent withdrawn. Cleanup is pending.' : 'Consent withdrawn. Shell cleanup is complete.'}</span> : <button type="button" className="btn" disabled={!!busy} onClick={() => {
        setBusy(consent.id)
        void apiFetch(`/api/sessions/${encodeURIComponent(session)}/shell-consent/withdraw`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ consent_id: consent.id }) }).then(async response => {
          if (!response.ok) throw new Error('Consent could not be withdrawn.')
          const result = await response.json() as { withdrawn?: boolean; cleanup_pending?: boolean }
          if (result.withdrawn !== true || typeof result.cleanup_pending !== 'boolean') throw new Error('Withdrawal could not be confirmed.')
          setItems(current => current.map(c => c.id === consent.id ? { ...c, withdrawn: true, cleanup_pending: result.cleanup_pending } : c))
          setMessage('')
        }).catch(error => setMessage(error instanceof Error ? error.message : 'Consent could not be withdrawn.')).finally(() => setBusy(''))
      }}>{busy === consent.id ? 'Withdrawing…' : 'Withdraw consent'}</button>}
    </div>)}
    {next && <button type="button" className="btn" disabled={!!busy} onClick={() => { void load(next) }}>Next consents</button>}
    {message && <p role="status">{message}</p>}
  </section>
}
