// McpSignIn: the action under an MCP server row that needs a sign-in ("!" rows, card #144).
//   - a claude.ai connector: "Connect" opens claude.ai's connector settings in a new tab; the
//     session loads it after a restart;
//   - a sandboxed session: sign-in there is coming (card #145);
//   - a server from the person's own settings (Claude Code's user scope; Codex's global config,
//     which the daemon checks) in a host session: "Sign in" asks the engine to start its own OAuth
//     sign-in and shows where the link goes: the ASCII host the browser visits, in bold (port
//     included), with a warning and the decoded form, small, for an internationalized (xn--) host;
//     opening it asks for a confirm (an alertdialog that takes the focus) naming the server, where it
//     was defined, the machine and the host. The engine listens for
//     the callback on the machine that runs the session and keeps the token itself;
//   - any other source (the project, the session, a plugin, anything unknown): no link; sign in
//     from a terminal. Such a server's authorization URL is chosen by whoever defined it.
// Data-driven: nothing here names an engine; the sources are what the row reports.
import { useEffect, useId, useRef, useState } from 'react'
import type { CapabilityItem } from '../types'
import { CLAUDE_AI_CONNECTORS_URL, startMcpSignIn, type SignInResult } from '../lib/pluginCatalog'
import { hostToUnicode } from '../lib/punycode'

export interface McpSignInContext {
  sessionId: string
  /** The session's runtime: "docker" and "cluster" are sandboxed. */
  runtime?: string
  /** The name of the machine that runs the session (its daemon), for the confirm. */
  machine?: string
}

// Sources Blerg offers a sign-in for: the person's own Claude Code user settings, and Codex's
// configured servers (the daemon signs in only to those in the person's global config.toml).
// "User config" is the person's own engine configuration on that machine, which an agent running
// there as the person could also have edited: the confirm says where the server was defined.
const SIGN_IN_SOURCES: Record<string, string> = { user: 'your user config', codex: 'your global Codex config' }

function terminalNote(source?: string): string {
  switch (source) {
    case 'plugin':
      return 'Sign in to this server from the terminal (it came with a plugin).'
    case 'managed':
    case 'enterprise':
      return 'Sign in to this server from the terminal (it is managed by your organization).'
    default:
      return 'Sign in to this server from the terminal (it was defined by the project).'
  }
}

export default function McpSignIn({ item, ctx }: { item: CapabilityItem; ctx: McpSignInContext }) {
  const [busy, setBusy] = useState(false)
  const [result, setResult] = useState<SignInResult | null>(null)
  const [confirming, setConfirming] = useState(false)
  const confirmRef = useRef<HTMLDivElement | null>(null)
  const openerRef = useRef<HTMLButtonElement | null>(null)
  const returnFocus = useRef(false)
  const titleId = useId()
  const descId = useId()
  // The confirm takes the focus when it opens; Cancel gives it back to "Open sign-in…".
  useEffect(() => {
    if (confirming) confirmRef.current?.focus()
    else if (returnFocus.current) {
      returnFocus.current = false
      openerRef.current?.focus()
    }
  }, [confirming])
  if (item.status !== 'needs-auth') return null
  if (item.source === 'claude.ai') {
    return (
      <div className="caps-auth" data-testid="caps-mcp-connect">
        <a className="caps-auth-btn" href={CLAUDE_AI_CONNECTORS_URL} target="_blank" rel="noopener noreferrer"
          aria-label={`Connect ${item.name} on claude.ai (opens in a new tab)`}>Connect ↗</a>
        <p className="caps-auth-note">After connecting, restart the session to load it.</p>
      </div>
    )
  }
  if (item.source === 'gateway') return null
  if (ctx.runtime === 'docker' || ctx.runtime === 'cluster') {
    return (
      <div className="caps-auth" data-testid="caps-mcp-sandboxed">
        <p className="caps-auth-note">Sign-in for sandboxed sessions is coming.</p>
      </div>
    )
  }
  const sourceLabel = item.source ? SIGN_IN_SOURCES[item.source] : undefined
  if (!sourceLabel || (result?.kind === 'error' && result.code === 'untrusted')) {
    return (
      <div className="caps-auth" data-testid="caps-mcp-terminal">
        <p className="caps-auth-note">{terminalNote(item.source)}</p>
      </div>
    )
  }
  async function start() {
    if (busy) return
    setBusy(true)
    setResult(null)
    setConfirming(false)
    setResult(await startMcpSignIn(ctx.sessionId, item.name))
    setBusy(false)
  }
  const target = result?.kind === 'url' ? new URL(result.url) : null
  // u.host: what the browser visits, port included when it is not the default.
  const raw = target?.host ?? ''
  const international = !!target && target.hostname.split('.').some(l => /^xn--/i.test(l))
  const decoded = international ? hostToUnicode(target.hostname) + (target.port ? `:${target.port}` : '') : ''
  const machine = ctx.machine || 'this machine'
  return (
    <div className="caps-auth" data-testid="caps-mcp-signin">
      {target ? (
        <>
          <span className="caps-auth-host" data-testid="caps-mcp-host">
            Opens <strong>{raw}</strong>
          </span>
          {international && (
            <p className="caps-auth-warn" data-testid="caps-mcp-idn">
              This address uses international characters; check it carefully. It is shown to people as <small>{decoded}</small>.
            </p>
          )}
          {!confirming && (
            <button ref={openerRef} type="button" className="caps-auth-btn" onClick={() => setConfirming(true)}
              aria-label={`Open the sign-in for ${item.name} at ${raw}`}>Open sign-in…</button>
          )}
          {confirming && (
            <div ref={confirmRef} className="caps-auth-confirm" role="alertdialog" aria-modal="false" tabIndex={-1}
              aria-labelledby={titleId} aria-describedby={descId} data-testid="caps-mcp-confirm"
              onKeyDown={e => { if (e.key === 'Escape') { e.stopPropagation(); returnFocus.current = true; setConfirming(false) } }}>
              <p className="caps-auth-note" id={titleId}>
                Sign in to <strong>{item.name}</strong> at <strong>{raw}</strong>?
              </p>
              <p className="caps-auth-note" id={descId}>
                Defined in {sourceLabel} on {machine}. Only continue if you added this server and expect this site
                {international ? ': its address uses international characters' : ''}.
              </p>
              <a className="caps-auth-btn" href={result?.kind === 'url' ? result.url : undefined} target="_blank" rel="noopener noreferrer"
                aria-label={`Open the sign-in at ${raw} (opens in a new tab)`} aria-describedby={descId} data-testid="caps-mcp-open-signin">Open ↗</a>
              <button type="button" className="caps-auth-btn" onClick={() => { returnFocus.current = true; setConfirming(false) }}>Cancel</button>
            </div>
          )}
        </>
      ) : (
        <button type="button" className="caps-auth-btn" disabled={busy} onClick={() => void start()} aria-label={`Sign in to ${item.name}`}>
          {busy ? 'Starting…' : 'Sign in'}
        </button>
      )}
      {result?.kind === 'url' && (
        <p className="caps-auth-note" role="status">
          {result.callback
            ? 'Finish in a browser on the machine that runs this session: the sign-in returns there. This row turns connected when it is done.'
            : 'Finish the sign-in in the new tab. This row turns connected when it is done.'}
        </p>
      )}
      {result?.kind === 'done' && <p className="caps-auth-note" role="status">Already signed in; the server reconnects.</p>}
      {result?.kind === 'error' && <p className="caps-auth-note failed" role="alert">{result.message}</p>}
    </div>
  )
}
