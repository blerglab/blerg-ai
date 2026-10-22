import { renderInFrame } from './mermaidFrame'
import { sanitizeSvg, validateMermaid } from './policy'
import { mermaidTheme, readVisualTheme, type VisualTheme } from './theme'

const CONFIG = {
  startOnLoad: false,
  securityLevel: 'strict' as const,
  suppressErrorRendering: true,
  maxTextSize: 16_384,
  maxEdges: 500,
  htmlLabels: false,
  flowchart: { htmlLabels: false },
  fontFamily: 'sans-serif',
  secure: ['secure', 'securityLevel', 'startOnLoad', 'maxTextSize', 'maxEdges',
    'suppressErrorRendering', 'htmlLabels', 'flowchart', 'themeCSS', 'fontFamily', 'theme', 'themeVariables'],
}

// One diagram at a time: the frame keeps Mermaid's global configuration (the theme) between
// initialize and render, and a hostile set of diagrams queues rather than piles up.
let pending: Promise<unknown> = Promise.resolve()
export function renderMermaid(source: string, container: HTMLElement, theme = readVisualTheme(container)): Promise<() => void> {
  // Mermaid gets exactly the string the validator approved (line endings and spaces normalized).
  let drawn: string
  try { drawn = validateMermaid(source) } catch (error) { return Promise.reject(error) }
  const job = pending.then(() => drawMermaid(drawn, container, theme))
  pending = job.catch(() => {})
  return job
}

async function drawMermaid(source: string, container: HTMLElement, theme: VisualTheme): Promise<() => void> {
  const drawn = validateMermaid(source)
  // Mermaid lays the diagram out in a sandboxed frame with no network (mermaidFrame.ts); only
  // its SVG text comes back, which is sanitized and shown as an inert image.
  let svg: string
  try {
    svg = await renderInFrame(drawn, {...CONFIG, ...mermaidTheme(theme), fontFamily: theme.fontFamily})
  } catch (error) {
    throw new Error(mermaidReason(error), { cause: error })
  }
  const image = document.createElement('img')
  image.alt = 'Mermaid diagram'
  image.className = 'visual-diagram'
  let clean: string
  try {
    clean = sanitizeSvg(svg)
  } catch (error) {
    throw new Error(`the drawn diagram was refused (${error instanceof Error ? error.message : 'unsupported SVG'})`, { cause: error })
  }
  const safe = new DOMParser().parseFromString(clean, 'image/svg+xml').documentElement
  const bounds = safe.getAttribute('viewBox')?.trim().split(/[\s,]+/).map(Number)
  if (bounds?.length === 4 && bounds.every(Number.isFinite) && bounds[2] > 0 && bounds[3] > 0) {
    // A percentage SVG width gives an <img> the browser's 300px fallback,
    // shrinking labels even in a wide message. Keep its natural aspect and
    // bound decoded image dimensions; CSS still scales it down to fit.
    const scale = Math.min(1, 4096 / Math.max(bounds[2], bounds[3]))
    safe.setAttribute('width', String(bounds[2] * scale))
    safe.setAttribute('height', String(bounds[3] * scale))
  }
  image.src = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(new XMLSerializer().serializeToString(safe))}`
  // Decode before displaying: CSP/image errors use the same source fallback as
  // parser/module failures, rather than leaving a broken image in the transcript.
  try {
    await image.decode()
  } catch {
    throw new Error('the browser could not display the drawn diagram')
  }
  container.replaceChildren(image)
  return () => image.remove()
}

/** Mermaid's own error, shortened to one line: "Mermaid: Parse error on line 3: Expecting …, got
 *  'LINK'". Its messages put the offending source and a caret on lines of their own. */
export function mermaidReason(error: unknown): string {
  const text = error instanceof Error ? error.message : typeof error === 'string' ? error : ''
  const lines = text.split('\n').map(line => line.trim()).filter(Boolean)
  if (!lines.length) return 'Mermaid could not draw it'
  const expecting = lines.find(line => /^Expecting\b/.test(line))
  const reason = [lines[0].replace(/[:.]$/, ''), expecting].filter(Boolean).join(': ')
  return `Mermaid: ${reason.length > 240 ? reason.slice(0, 239) + '…' : reason}`
}
