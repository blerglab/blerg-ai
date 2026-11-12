/** Bound serialization before allocating the final string. A structured-clone message is
 *  untrusted: cycles, non-JSON types, deep data, and aggregate oversize values are rejected. */
export const ANSWER_MAX_BYTES = 16 * 1024
export function answerResult(data: unknown): string | null {
  if (!data || typeof data !== 'object' || (data as {type?:unknown}).type !== 'blerg:answer') return null
  let nodes = 0, bytes = 0
  const ancestors = new Set<object>()
  const chunks: string[] = []
  const encoder = new TextEncoder()
  const write = (s: string): boolean => {
    bytes += s.length > ANSWER_MAX_BYTES ? ANSWER_MAX_BYTES + 1 : encoder.encode(s).length
    if (bytes > ANSWER_MAX_BYTES) return false
    chunks.push(s)
    return true
  }
  const quoted = (s: string) => s.length <= ANSWER_MAX_BYTES && write(JSON.stringify(s))
  function json(v: unknown, depth: number): boolean {
    if (++nodes > 8192 || depth > 16) return false
    if (v === null || typeof v === 'boolean') return write(String(v))
    if (typeof v === 'string') return quoted(v)
    if (typeof v === 'number') return Number.isFinite(v) && write(JSON.stringify(v))
    if (typeof v !== 'object' || ancestors.has(v)) return false
    const array = Array.isArray(v)
    if (!array && Object.getPrototypeOf(v) !== Object.prototype && Object.getPrototypeOf(v) !== null) return false
    ancestors.add(v)
    if (!write(array ? '[' : '{')) return false
    let count = 0
    if (array) {
      if (v.length > 8192) return false
      for (const item of v) {
        if ((count++ > 0 && !write(',')) || !json(item, depth + 1)) return false
      }
    } else {
      for (const key in v) {
        if (!Object.hasOwn(v, key)) continue
        if (++count > 8192 || (count > 1 && !write(',')) || !quoted(key) || !write(':') || !json((v as Record<string, unknown>)[key], depth + 1)) return false
      }
    }
    ancestors.delete(v)
    return write(array ? ']' : '}')
  }
  return json((data as {result?:unknown}).result, 0) ? chunks.join('') : null
}
