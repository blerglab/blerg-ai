import mermaid from 'mermaid'
import { sanitizeSvg, validateMermaid } from './policy'
import { mermaidTheme, readVisualTheme, type VisualTheme } from './theme'

let serial = 0
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

// initialize mutates Mermaid's global config outside its internal render queue.
// Keep both operations together, even when separate chat roots use different themes.
let pending: Promise<unknown> = Promise.resolve()
export function renderMermaid(source: string, container: HTMLElement, theme = readVisualTheme(container)): Promise<() => void> {
  try { validateMermaid(source) } catch (error) { return Promise.reject(error) }
  const job = pending.then(() => drawMermaid(source, container, theme))
  pending = job.catch(() => {})
  return job
}

async function drawMermaid(source: string, container: HTMLElement, theme: VisualTheme): Promise<() => void> {
  validateMermaid(source)
  // Mermaid measures text in a temporary DOM tree. Only validated, strict-mode source
  // enters it; the final image has no DOM, callbacks or active resource references.
  const scratch = document.createElement('div')
  scratch.style.cssText = 'position:fixed;left:-100000px;top:0;visibility:hidden;pointer-events:none'
  scratch.setAttribute('aria-hidden', 'true')
  document.body.append(scratch)
  try {
    mermaid.initialize({...CONFIG, ...mermaidTheme(theme), fontFamily: theme.fontFamily})
    const { svg } = await mermaid.render(`blerg-diagram-${++serial}`, source, scratch)
    const image = document.createElement('img')
    image.alt = 'Mermaid diagram'
    image.className = 'visual-diagram'
    const safe = new DOMParser().parseFromString(sanitizeSvg(svg), 'image/svg+xml').documentElement
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
    await image.decode()
    container.replaceChildren(image)
    return () => image.remove()
  } finally {
    scratch.remove()
  }
}
