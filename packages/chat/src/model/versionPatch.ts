// Patience anchors and bounded line LCS preserve context without quadratic work on large documents.
// A large changed middle is represented as a replacement; the patch remains exact.
export function versionPatch(before: string, after: string, name: string): string {
  if (before === after) return ''
  const lines = (s: string) => s.match(/[^\n]*\n|[^\n]+$/g) ?? []
  const a = lines(before), b = lines(after)
  const out: string[] = []
  function emit(prefix: string, line: string) {
    out.push(prefix + line.replace(/\n$/, ''))
    if (!line.endsWith('\n')) out.push('\\ No newline at end of file')
  }
  let start = 0
  while (start < a.length && start < b.length && a[start] === b[start]) emit(' ', a[start++])
  let end = 0
  while (end < a.length - start && end < b.length - start && a[a.length - end - 1] === b[b.length - end - 1]) end++
  const x = a.slice(start, a.length - end), y = b.slice(start, b.length - end)
  function diff(x: string[], y: string[], depth = 0) {
    if ((x.length + 1) * (y.length + 1) <= 1_000_000) {
      const rows = Array.from({ length: x.length + 1 }, () => new Uint32Array(y.length + 1))
      for (let i = x.length - 1; i >= 0; i--) for (let j = y.length - 1; j >= 0; j--) {
        rows[i][j] = x[i] === y[j] ? rows[i + 1][j + 1] + 1 : Math.max(rows[i + 1][j], rows[i][j + 1])
      }
      let i = 0, j = 0
      while (i < x.length || j < y.length) {
        if (i < x.length && j < y.length && x[i] === y[j]) { emit(' ', x[i++]); j++ }
        else if (i < x.length && (j === y.length || rows[i + 1][j] >= rows[i][j + 1])) emit('-', x[i++])
        else emit('+', y[j++])
      }
    } else {
      const anchors = depth < 16 ? commonAnchors(x, y) : []
      if (!anchors.length) { x.forEach(l => emit('-', l)); y.forEach(l => emit('+', l)) }
      else {
        let i = 0, j = 0
        for (const [nextI, nextJ] of anchors) {
          if (i !== nextI || j !== nextJ) diff(x.slice(i, nextI), y.slice(j, nextJ), depth + 1)
          emit(' ', x[nextI]); i = nextI + 1; j = nextJ + 1
        }
        if (i !== x.length || j !== y.length) diff(x.slice(i), y.slice(j), depth + 1)
      }
    }
  }
  diff(x, y)
  a.slice(a.length - end).forEach(l => emit(' ', l))
  // JSON quoting is the git patch path quoting accepted by our parser.
  return `--- ${JSON.stringify(`a/${name}`)}\n+++ ${JSON.stringify(`b/${name}`)}\n@@ -${a.length ? 1 : 0},${a.length} +${b.length ? 1 : 0},${b.length} @@\n${out.join('\n')}\n`
}


// Unique lines shared by both sides, in their longest increasing order. These
// split a large document into small changed ranges before the bounded LCS.
function commonAnchors(x: string[], y: string[]): [number, number][] {
  function unique(lines: string[]) {
    const positions = new Map<string, number>()
    lines.forEach((line, i) => positions.set(line, positions.has(line) ? -1 : i))
    return positions
  }
  const left = unique(x), right = unique(y)
  const pairs: [number, number][] = []
  x.forEach((line, i) => {
    const j = right.get(line)
    if (left.get(line) === i && j !== undefined && j >= 0) pairs.push([i, j])
  })
  const tails: number[] = [], parents: number[] = []
  pairs.forEach((pair, i) => {
    let lo = 0, hi = tails.length
    while (lo < hi) {
      const mid = (lo + hi) >>> 1
      if (pairs[tails[mid]][1] < pair[1]) lo = mid + 1
      else hi = mid
    }
    parents[i] = lo ? tails[lo - 1] : -1
    tails[lo] = i
  })
  const result: [number, number][] = []
  for (let i = tails.at(-1) ?? -1; i >= 0; i = parents[i]) result.push(pairs[i])
  return result.reverse()
}
