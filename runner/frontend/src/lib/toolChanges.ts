// Tool changes (/proposals, "Tool changes" section): how a changed or new MCP tool definition is
// shown to the person who must decide on it. Everything here produces plain strings: upstream text
// is untrusted and is never rendered as markdown or HTML, and characters that render as nothing
// (bidi controls, tag characters, zero-width and private-use code points) are made visible, so
// the person reads what the model would read.
import type { ToolChangeInfo, ToolDefinitionInfo } from '../types'
import { prettyJson } from './proposals'

// hiddenRe matches every code point that must be shown as an escape: control (but not \n and
// \t), format, private-use and unassigned characters, the line/paragraph separators, and the
// tag block. `u` makes \p{} work; `g` for replaceAll-style iteration.
const hiddenRe = /[\p{Cc}\p{Cf}\p{Co}\p{Cn}\u2028\u2029\u{E0000}-\u{E007F}]/gu

// escapeHidden replaces every hidden code point with ⟨U+XXXX⟩ and says whether it found any.
export function escapeHidden(s: string): { text: string; hidden: boolean } {
  let hidden = false
  const text = s.replace(hiddenRe, ch => {
    if (ch === '\n' || ch === '\t') return ch
    hidden = true
    return `⟨U+${ch.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')}⟩`
  })
  return { text, hidden }
}

// isPlainName: a tool name made only of printable ASCII. Anything else is flagged on the card,
// because a look-alike name is how one tool is passed off as another.
export const isPlainName = (name: string) => /^[\x20-\x7E]+$/.test(name)

export interface DiffPart { kind: 'same' | 'add' | 'del'; text: string }

// lcsDiff is a plain longest-common-subsequence diff over tokens (O(n·m), bounded by the caller).
function lcsDiff(a: string[], b: string[]): DiffPart[] {
  const n = a.length, m = b.length
  const dp: Int32Array[] = Array.from({ length: n + 1 }, () => new Int32Array(m + 1))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }
  const out: DiffPart[] = []
  let i = 0, j = 0
  const push = (kind: DiffPart['kind'], text: string) => {
    const last = out[out.length - 1]
    if (last && last.kind === kind) last.text += text
    else out.push({ kind, text })
  }
  while (i < n && j < m) {
    if (a[i] === b[j]) { push('same', a[i]); i++; j++ }
    else if (dp[i + 1][j] >= dp[i][j + 1]) { push('del', a[i]); i++ }
    else { push('add', b[j]); j++ }
  }
  while (i < n) push('del', a[i++])
  while (j < m) push('add', b[j++])
  return out
}

const MAX_TOKENS = 4000

// tooBig: the LCS table is n·m cells; past this the texts are shown as one removal and one
// addition instead of being diffed (a 256 KiB definition must never freeze the tab).
const tooBig = (n: number, m: number) => n > MAX_TOKENS || m > MAX_TOKENS || n * m > 1_000_000

// wordDiff diffs two descriptions word by word; whitespace runs are their own tokens, so a word
// followed by new text is still "same".
export function wordDiff(a: string, b: string): DiffPart[] {
  const tok = (s: string) => s.match(/\s+|\S+/g) ?? []
  const ta = tok(a), tb = tok(b)
  if (tooBig(ta.length, tb.length)) return [{ kind: 'del', text: a }, { kind: 'add', text: b }]
  return lcsDiff(ta, tb)
}

// lineDiff diffs two pretty-printed schemas line by line; each part is one line (no newline).
export function lineDiff(a: string, b: string): DiffPart[] {
  const la = a.split('\n'), lb = b.split('\n')
  if (tooBig(la.length, lb.length)) return [{ kind: 'del', text: a }, { kind: 'add', text: b }]
  const out: DiffPart[] = []
  for (const p of lcsDiffLines(la, lb)) out.push(p)
  return out
}

function lcsDiffLines(a: string[], b: string[]): DiffPart[] {
  // Same algorithm as lcsDiff but without merging neighbours: one part per line.
  const n = a.length, m = b.length
  const dp: Int32Array[] = Array.from({ length: n + 1 }, () => new Int32Array(m + 1))
  for (let i = n - 1; i >= 0; i--) for (let j = m - 1; j >= 0; j--) dp[i][j] = a[i] === b[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
  const out: DiffPart[] = []
  let i = 0, j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) { out.push({ kind: 'same', text: a[i] }); i++; j++ }
    else if (dp[i + 1][j] >= dp[i][j + 1]) { out.push({ kind: 'del', text: a[i] }); i++ }
    else { out.push({ kind: 'add', text: b[j] }); j++ }
  }
  while (i < n) out.push({ kind: 'del', text: a[i++] })
  while (j < m) out.push({ kind: 'add', text: b[j++] })
  return out
}

// schemaText is the input schema as it was hashed, re-indented from the canonical text so number
// lexemes survive; 'null' when there is none.
export function schemaText(def: ToolDefinitionInfo | null | undefined): string {
  if (!def || !def.canonical) return 'null'
  // The canonical object has the keys description, inputSchema, name in that order; take the
  // inputSchema value's text by parsing positions, not values.
  const key = '"inputSchema":'
  const start = def.canonical.indexOf(key)
  if (start < 0) return 'null'
  const from = start + key.length
  const end = def.canonical.lastIndexOf(',"name":')
  if (end <= from) return 'null'
  const raw = def.canonical.slice(from, end)
  if (raw === 'null') return 'null'
  return prettyJson(raw)?.text ?? raw
}

export function unsafeReason(u: string | undefined): string | null {
  switch (u) {
    case 'hidden_chars': return 'This definition contains hidden characters (direction overrides or tag characters) that a model would read but a person cannot see. It cannot be reviewed or accepted.'
    case 'too_large': return 'This definition is too large to review (over 256 KiB). It cannot be accepted.'
    case 'nul': return 'This definition contains characters that cannot be stored, so it cannot be reviewed or accepted.'
    default: return null
  }
}

export function acceptFailureMessage(status: number, serverMessage: string): string {
  switch (status) {
    case 409: return `Not accepted: ${serverMessage}`
    case 410: return `Not accepted: ${serverMessage}. The tool stays off; a new session will list the current definition.`
    case 400: return `Not accepted: ${serverMessage}`
    case 401: return 'Your sign-in has ended. Sign in again, then accept.'
    case 503: return 'The MCP gateway is not configured on this server, so tool changes cannot be accepted.'
    default: return serverMessage
  }
}

// Shared fetch shapes.
export interface ToolChangesListBody { tool_changes?: ToolChangeInfo[]; decided_next?: string | null; pending_count?: number }

// The number a session's capabilities panel shows: changes waiting on a decision, like the
// badge — unmuted, and never `removed` rows (informational, nothing to decide).
export function countPendingToolChanges(list: { muted?: boolean; kind?: string }[] | undefined): number {
  return (list ?? []).filter(c => !c.muted && c.kind !== 'removed').length
}
