import type { Anchor } from './review'

/** Shared source coordinates for workspace reviews and inline diagnostics. Lines are inclusive. */
export interface WorkspaceAnchor { path: string; start: number; end: number; contentHash: string }

export function validWorkspacePath(path: unknown): path is string {
  return typeof path === 'string' && path.length > 0 && path.length <= 4096 &&
    !/[\p{Cc}\p{Cf}\\]/u.test(path) && !path.startsWith('/') && !/^[a-z]:/i.test(path) &&
    !path.split('/').some(part => part === '..' || part === '' || part === '.')
}

export function parseWorkspaceAnchor(value: unknown): WorkspaceAnchor | null {
  if (!value || typeof value !== 'object') return null
  const a = value as Partial<WorkspaceAnchor>
  if (!validWorkspacePath(a.path) || !Number.isSafeInteger(a.start) || !Number.isSafeInteger(a.end) ||
    a.start! < 0 || a.end! < a.start! || (a.start === 0 && a.end !== 0) ||
    typeof a.contentHash !== 'string' || !/^[a-f0-9]{64}$/.test(a.contentHash)) return null
  return {path:a.path, start:a.start!, end:a.end!, contentHash:a.contentHash}
}

export async function workspaceContentHash(bytes: string | Blob): Promise<string> {
  const data = typeof bytes === 'string' ? new TextEncoder().encode(bytes) : await bytes.arrayBuffer()
  return [...new Uint8Array(await crypto.subtle.digest('SHA-256', data))].map(b => b.toString(16).padStart(2, '0')).join('')
}

/** 0–0 identifies a whole binary file, whose bytes are hashed by the caller. */
export async function createWorkspaceAnchor(path: string, text: string, start: number, end: number): Promise<Anchor> {
  const lines = text.split('\n')
  if (!validWorkspacePath(path) || !Number.isSafeInteger(start) || !Number.isSafeInteger(end) ||
    start < 1 || end < start || end > lines.length) throw new Error('Invalid workspace selection')
  const quote = lines.slice(start - 1, end).join('\n')
  if (quote.length > 32768) throw new Error('Select a shorter passage (up to 32,768 characters).')
  return {kind:'text', quote, workspace:{path, start, end, contentHash:await workspaceContentHash(text)}}
}

/** Relocate only a unique exact sequence of complete lines; ambiguous or changed code is outdated. */
export async function projectWorkspaceAnchor(anchor: Anchor, text: string): Promise<{start:number;end:number;moved:boolean} | null> {
  const a = anchor.workspace
  if (!a || a.start === 0 || anchor.quote === undefined) return null
  const lines = text.split('\n')
  if (await workspaceContentHash(text) === a.contentHash) {
    return a.end <= lines.length && lines.slice(a.start - 1, a.end).join('\n') === anchor.quote
      ? {start:a.start, end:a.end, moved:false} : null
  }
  const count = a.end - a.start + 1
  const needle = '\n' + anchor.quote + '\n'
  const haystack = '\n' + text + '\n'
  const at = haystack.indexOf(needle)
  if (at < 0 || haystack.indexOf(needle, at + 1) >= 0) return null
  const start = haystack.slice(0, at + 1).split('\n').length - 1
  return {start, end:start + count - 1, moved:start !== a.start}
}
