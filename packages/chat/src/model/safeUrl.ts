const SAFE_PROTOCOLS = new Set(['http:', 'https:', 'mailto:'])

/** safeUrl returns the URL when it is absolute http(s)/mailto, else null —
 *  javascript:, data:, vbscript:, relative and malformed URLs are refused. */
export function safeUrl(raw: string | null | undefined): string | null {
  if (!raw) return null
  const trimmed = raw.trim()
  let u: URL
  try {
    u = new URL(trimmed)
  } catch {
    return null // relative or malformed: nothing sensible to link to
  }
  return SAFE_PROTOCOLS.has(u.protocol) ? trimmed : null
}
