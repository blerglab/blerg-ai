/** Greedy measured wrapping, including a bounded split of long unbroken labels. The caller
 * supplies metrics from the resolved font (SVG on the page, canvas in the worker). */
export function wrapMeasured(value: string, width: number, measure: (text: string) => number): string[] {
  const lines: string[] = []
  let line = ''
  for (const word of value.split(/\s+/).filter(Boolean)) {
    const candidate = line ? `${line} ${word}` : word
    if (measure(candidate) <= width) { line = candidate; continue }
    if (line) { lines.push(line); line = '' }
    for (const character of word) {
      if (line && measure(line + character) > width) { lines.push(line); line = '' }
      line += character
    }
  }
  if (line) lines.push(line)
  return lines
}
