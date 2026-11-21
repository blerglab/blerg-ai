import { useEffect, useMemo, useState } from 'react'
import { ShellReport, shellReportVersions } from '../shellReport'
import { apiFetch } from '../apiFetch'
import type { ShellTransport } from '../shellTransport'

export default function ShellDetails({ transport, session }: { transport: Pick<ShellTransport, 'info' | 'report' | 'refreshDetails'>; session: string }) {
  const report = useMemo(() => transport.report ?? new ShellReport(transport.info, session), [transport, session])
  const [copied, setCopied] = useState(false)
  const [failed, setFailed] = useState(false)
  const [ready, setReady] = useState<ShellReport>()
  useEffect(() => {
    let active = true
    const abort = new AbortController()
    const refresh = async () => {
      try {
        if (transport.refreshDetails) await transport.refreshDetails()
        else if (transport.info.handle) {
          const response = await apiFetch(`/api/shells/${encodeURIComponent(transport.info.handle)}/details`, { signal: AbortSignal.any([abort.signal, AbortSignal.timeout(3000)]) })
          if (response.ok) report.reason((await response.json() as { reason?: unknown }).reason)
        } else await Promise.resolve()
      } catch { /* Local diagnostics remain available during an outage. */ }
      finally { if (active) setReady(report) }
    }
    void refresh()
    return () => { active = false; abort.abort() }
  }, [transport, report])
  const copy = async () => {
    const text = report.text(shellReportVersions(session))
    try { await navigator.clipboard.writeText(text); setCopied(true); setFailed(false) } catch { setFailed(true) }
  }
  return <><button type="button" className="btn" disabled={ready !== report} onClick={() => { void copy() }}>{copied ? 'Copied' : 'Copy details'}</button>{failed && <span role="alert">Could not copy details. Allow clipboard access and try again.</span>}</>
}
