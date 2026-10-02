// Helpers for MCP proposals (spec 9): how the frozen arguments and an upstream result are shown,
// which proposals wait on the person, what a refused approval says, and the shared pending count
// behind the nav badge. Everything here produces plain strings: upstream output is untrusted and
// is never rendered as markdown or HTML.
import { useEffect } from 'react'
import { create } from 'zustand'
import { apiFetch } from '../apiFetch'
import type { ProposalResult, ProposalInfo } from '../types'

// A string value longer than this is cut in the collapsed view, with a "Show all" action.
export const ARG_COLLAPSE_CHARS = 400

const NUMBER_LEXEME = /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/
const MAX_DEPTH = 100

// prettyJson re-indents JSON TEXT without turning it into values: every string and number is
// copied through as written (so 12345678901234567890 and 1.10 stay as they are, and key order is
// the text's). A string whose decoded length exceeds `maxString` is cut when that is set. Returns
// null when the text is not valid JSON.
export function prettyJson(raw: string, maxString?: number): { text: string; truncated: boolean } | null {
  let i = 0
  let truncated = false
  const ws = () => { while (i < raw.length && ' \t\n\r'.includes(raw[i])) i++ }
  const fail = (): never => { throw new SyntaxError('invalid JSON') }

  function str(): string {
    const start = i
    i++ // opening quote
    while (i < raw.length && raw[i] !== '"') {
      if (raw.charCodeAt(i) < 0x20) fail()
      i += raw[i] === '\\' ? 2 : 1
    }
    if (i >= raw.length) fail()
    i++
    const lexeme = raw.slice(start, i)
    let decoded: string
    try { decoded = JSON.parse(lexeme) as string } catch { return fail() }
    if (maxString !== undefined && decoded.length > maxString) {
      truncated = true
      return JSON.stringify(`${decoded.slice(0, maxString)}… (${decoded.length - maxString} more characters)`)
    }
    return lexeme
  }

  function value(depth: number, indent: string): string {
    if (depth > MAX_DEPTH) return fail()
    ws()
    const c = raw[i]
    if (c === '"') return str()
    if (c === '{' || c === '[') {
      const close = c === '{' ? '}' : ']'
      i++
      ws()
      if (raw[i] === close) { i++; return c + close }
      const inner = `${indent}  `
      const parts: string[] = []
      for (;;) {
        ws()
        if (c === '{') {
          if (raw[i] !== '"') fail()
          const key = str()
          ws()
          if (raw[i] !== ':') fail()
          i++
          parts.push(`${inner}${key}: ${value(depth + 1, inner)}`)
        } else {
          parts.push(`${inner}${value(depth + 1, inner)}`)
        }
        ws()
        if (raw[i] === ',') { i++; continue }
        if (raw[i] === close) { i++; break }
        fail()
      }
      return `${c}\n${parts.join(',\n')}\n${indent}${close}`
    }
    const m = /^[^\s,:\]}[{"]+/.exec(raw.slice(i, i + 4096))
    if (!m) return fail()
    const lexeme = m[0]
    if (lexeme !== 'true' && lexeme !== 'false' && lexeme !== 'null' && !NUMBER_LEXEME.test(lexeme)) return fail()
    i += lexeme.length
    return lexeme
  }

  try {
    const text = value(0, '')
    ws()
    if (i !== raw.length) return null
    return { text, truncated }
  } catch {
    return null
  }
}

// The fields the arguments views read: the stored JSON text (authoritative) and the parsed object
// (an older server sends only this).
type ArgSource = Pick<ProposalInfo, 'arguments_raw'> & { arguments?: Record<string, unknown> | null }

function rawOf(p: ArgSource): string {
  return typeof p.arguments_raw === 'string' ? p.arguments_raw : JSON.stringify(p.arguments ?? {})
}

// formatArguments is the frozen arguments as the gateway will send them, re-indented only. If the
// text cannot be formatted it is shown as it is.
export function formatArguments(p: ArgSource): string {
  const raw = rawOf(p)
  return prettyJson(raw)?.text ?? raw
}

// collapsedArguments is the same with very long strings cut; `truncated` says whether anything
// was, so the card can offer the full text. The approve step never uses it.
export function collapsedArguments(p: ArgSource): { text: string; truncated: boolean } {
  const raw = rawOf(p)
  return prettyJson(raw, ARG_COLLAPSE_CHARS) ?? { text: raw, truncated: false }
}

export function resultText(r: ProposalResult): { text: string; isError: boolean } | null {
  if (!r) return null
  if ('error' in r && typeof r.error === 'string') return { text: r.error, isError: true }
  const content = 'content' in r && Array.isArray(r.content) ? r.content : []
  const text = content.filter(c => c.type === 'text' && typeof c.text === 'string').map(c => c.text).join('\n')
  if (!text) return null
  return { text, isError: 'isError' in r && r.isError === true }
}

// A proposal the person still has something to do about.
export const isAwaitingYou = (p: ProposalInfo) => p.state === 'pending' || p.state === 'executing' || p.state === 'unknown'

// approveFailureMessage explains a refused approval. The server's words decide which 409 it is.
export function approveFailureMessage(status: number, serverMessage: string): string {
  const m = serverMessage.toLowerCase()
  switch (status) {
    case 409:
      if (m.includes('no longer pending') || m.includes('already decided')) {
        return `Not run. This proposal was already decided or is running (${serverMessage}). The list has been refreshed.`
      }
      if (m.includes('connection no longer exists')) {
        return 'Not run. The connection this proposal used no longer exists, so it can never run. Reject it, or ask the agent to propose again on another connection.'
      }
      if (m.includes('expired')) {
        return 'Not run. This proposal has expired (proposals last 7 days). Ask the agent to propose it again.'
      }
      if (m.includes('changed')) {
        return `Not run. The connection's address or the tool's definition changed after the agent proposed this, so the frozen call may no longer mean what it did. Reject this proposal and ask the agent to propose again. (${serverMessage})`
      }
      return `Not run. The proposal is still pending: ${serverMessage}`
    case 401: return 'Your sign-in has ended. Sign in again, then approve.'
    case 502: return `Nothing was sent: it is back in your pending list, try again shortly. (${serverMessage})`
    case 503: return 'The MCP gateway is not configured on this server, so proposals cannot be run.'
    default: return serverMessage
  }
}

// ---- pending count (the nav badge) ---------------------------------------------------------------------

export const usePendingProposals = create<{ count: number }>(() => ({ count: 0 }))

export const PROPOSALS_POLL_MS = 30_000

// A request takes a ticket when it STARTS; its count is applied only if no newer request has
// already applied one, so a slow old response never overwrites a newer answer. The badge poll and
// the Proposals page share the tickets.
let countTicket = 0
let countApplied = 0
export const beginCountRequest = () => ++countTicket
export function applyPendingCount(ticket: number, n: number): boolean {
  if (ticket < countApplied) return false
  countApplied = ticket
  usePendingProposals.setState({ count: n })
  return true
}

// fetchPendingCount reads the cheap count route. A failure leaves the last count.
export async function fetchPendingCount(): Promise<void> {
  const ticket = beginCountRequest()
  try {
    const res = await apiFetch('/api/proposals/count')
    if (!res.ok) return
    const body = await res.json() as { pending_count?: number }
    if (typeof body.pending_count === 'number') applyPendingCount(ticket, body.pending_count)
  } catch { /* keep the last count */ }
}

// useProposalCountPoll keeps the badge count fresh: now, every 30 s, and when the window regains
// focus or becomes visible. It does nothing while the tab is hidden.
export function useProposalCountPoll() {
  useEffect(() => {
    const tick = () => { if (!document.hidden) void fetchPendingCount() }
    tick()
    const timer = setInterval(tick, PROPOSALS_POLL_MS)
    window.addEventListener('focus', tick)
    document.addEventListener('visibilitychange', tick)
    return () => {
      clearInterval(timer)
      window.removeEventListener('focus', tick)
      document.removeEventListener('visibilitychange', tick)
    }
  }, [])
}
