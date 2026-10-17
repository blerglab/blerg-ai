// The line cap on code and pasted text: a block over CAP_OVER lines shows its first CAP_SHOW.

/** A block longer than this many lines is compacted. */
export const CAP_OVER = 25
/** A compacted block shows this many lines. */
export const CAP_SHOW = 15

/** The number of lines of `text` (a trailing newline does not start a line). */
export function countLines(text: string): number {
  if (text === '') return 0
  const n = text.split('\n').length
  return text.endsWith('\n') ? n - 1 : n
}

/** The first `n` lines of `text`, without a trailing newline. */
export function firstLines(text: string, n: number): string {
  return text.split('\n').slice(0, n).join('\n')
}

/** Whether a user's message is long enough to compact as a whole. A closed fence that is
 *  already over the cap compacts itself, so it counts as one line here, not as many. */
export function messageNeedsCap(text: string): boolean {
  if (text.length < CAP_OVER) return false
  const flat = text.replace(/^ {0,3}(`{3,}|~{3,})[^\n]*\n([\s\S]*?)\n {0,3}\1[ \t]*$/gm, (m, _f, body: string) =>
    countLines(body) > CAP_OVER ? 'code' : m)
  return countLines(flat) > CAP_OVER
}
