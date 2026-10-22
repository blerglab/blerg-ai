// Punycode (RFC 3492) decoding of a host name's xn-- labels, so a sign-in link can show the host as
// a person would read it next to the raw ASCII form a browser actually visits (card #144: a
// look-alike internationalized host must not pass for the real one). Decoding only; a label that
// does not decode stays as it is.

const BASE = 36
const TMIN = 1
const TMAX = 26
const SKEW = 38
const DAMP = 700
const INITIAL_BIAS = 72
const INITIAL_N = 128

function adapt(delta: number, numPoints: number, first: boolean): number {
  let d = first ? Math.floor(delta / DAMP) : delta >> 1
  d += Math.floor(d / numPoints)
  let k = 0
  while (d > ((BASE - TMIN) * TMAX) >> 1) {
    d = Math.floor(d / (BASE - TMIN))
    k += BASE
  }
  return k + Math.floor(((BASE - TMIN + 1) * d) / (d + SKEW))
}

function digit(c: number): number {
  if (c >= 0x30 && c <= 0x39) return c - 0x30 + 26 // 0-9
  if (c >= 0x41 && c <= 0x5a) return c - 0x41 // A-Z
  if (c >= 0x61 && c <= 0x7a) return c - 0x61 // a-z
  return BASE
}

/** Decodes one punycode label body (without "xn--"); null when it is not valid punycode. */
export function decodeLabel(input: string): string | null {
  const out: number[] = []
  const basic = input.lastIndexOf('-')
  for (let j = 0; j < Math.max(basic, 0); j++) {
    const c = input.charCodeAt(j)
    if (c >= 0x80) return null
    out.push(c)
  }
  let n = INITIAL_N
  let bias = INITIAL_BIAS
  let i = 0
  for (let idx = basic > 0 ? basic + 1 : 0; idx < input.length;) {
    const oldi = i
    let w = 1
    for (let k = BASE; ; k += BASE) {
      if (idx >= input.length) return null
      const d = digit(input.charCodeAt(idx++))
      if (d >= BASE) return null
      i += d * w
      if (i > 0x10ffff * 64) return null
      const t = k <= bias ? TMIN : k >= bias + TMAX ? TMAX : k - bias
      if (d < t) break
      w *= BASE - t
    }
    bias = adapt(i - oldi, out.length + 1, oldi === 0)
    n += Math.floor(i / (out.length + 1))
    i %= out.length + 1
    if (n > 0x10ffff) return null
    out.splice(i++, 0, n)
  }
  try {
    return String.fromCodePoint(...out)
  } catch {
    return null
  }
}

/** The host with each xn-- label decoded. */
export function hostToUnicode(host: string): string {
  return host
    .split('.')
    .map(label => {
      if (!/^xn--/i.test(label)) return label
      const decoded = decodeLabel(label.slice(4))
      return decoded ?? label
    })
    .join('.')
}
