/** One shape and color for each execution type, wherever a session is shown. */
import { runtimeSentence } from '../lib/runtimes'
export default function RuntimeIcon({ runtime, machine, className = '' }: { runtime?: string; machine?: string; className?: string }) {
  const type = runtime === 'daemon' || runtime === 'host' ? 'host' : runtime === 'docker' || runtime === 'sandbox' ? 'sandbox' : runtime === 'cluster' ? 'cluster' : 'unknown'
  const label = runtimeSentence(runtime, machine)
  const shape = type === 'host' ? <><rect x="3" y="3" width="18" height="13" rx="2" /><path d="M12 16v5m-5 0h10" /></> : type === 'sandbox' ? <><path d="m12 2 9 5v10l-9 5-9-5V7zM3 7l9 5 9-5M12 12v10" /></> : type === 'cluster' ? <path d="M6 19a5 5 0 1 1 1-10 6 6 0 0 1 11-2 6 6 0 0 1 0 12Z" /> : <><circle cx="12" cy="12" r="9" /><path d="M12 8v5m0 3v1" /></>
  return <span role="img" aria-label={label} title={label} data-runtime={type} className={`runtime-icon ${className}`} style={{ display: 'inline-flex', flexShrink: 0, verticalAlign: 'middle', color: type === 'host' ? 'var(--amber)' : type === 'sandbox' ? 'var(--lichen)' : type === 'cluster' ? 'var(--batch)' : 'var(--fog)' }}><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" aria-hidden="true">{shape}</svg></span>
}
