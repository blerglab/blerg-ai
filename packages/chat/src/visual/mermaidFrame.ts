// Mermaid draws in a frame of its own, so a diagram the validator missed still cannot reach the
// network or the page. Mermaid has to lay text out in a live document to measure it, and whatever
// it puts in that document is live: an <image href> is fetched, a <style> applies. So that
// document is a sandboxed iframe:
//   - sandbox="allow-scripts" without allow-same-origin: an opaque origin, with no cookies, no
//     storage and no access to this page; no popups, navigation or forms;
//   - a Content-Security-Policy in its own head, before any script: default-src 'none', so no
//     image, font, style sheet, fetch or frame can load from anywhere (data: images only);
//   - it only talks to this page over a MessageChannel port handed over once, when its own
//     document has loaded: a diagram's source in, an SVG string out; a frame that loads anything
//     else later (it navigated) is dropped, never reused. The
//     SVG is then sanitized here and shown as an inert <img> (mermaid.ts).
// One frame serves every diagram, created on first use; renders are queued one at a time.
import mermaidScript from 'mermaid/dist/mermaid.min.js?raw'

export const MERMAID_FRAME_CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'"

// Runs inside the frame: waits for the port its parent hands over once, then answers render
// requests on that port only, with the SVG or the error.
const BOOT = `
(() => {
  const scratch = document.getElementById('scratch')
  // Some renderers (event modeling) hardcode XHTML labels and ignore htmlLabels:false.
  // Convert their already laid-out boxes to measured SVG text inside the isolated document.
  // No XHTML crosses the port or reaches the image; the parent still sanitizes every SVG.
  function textLabels(source, host, config) {
    const doc = new DOMParser().parseFromString(source, 'image/svg+xml')
    // Mermaid's XHTML may contain HTML-only <br> or named entities. Parse that form as HTML
    // before serializing valid XML; the parent's SVG allowlist still checks the final result.
    const root = doc.querySelector('parsererror') && source.includes('<foreignObject')
      ? new DOMParser().parseFromString(source, 'text/html').querySelector('svg')
      : doc.documentElement
    if (!root) return source
    const labels = [...root.querySelectorAll('foreignObject')]
    if (!labels.length) return source
    host.append(root)
    const ns = 'http://www.w3.org/2000/svg'
    for (const label of labels) {
      const content = label.querySelector('div')
      const lines = (content ? content.innerText : label.textContent).split('\\n').map(line => line.trim()).filter(Boolean)
      const x = Number(label.getAttribute('x') || 0)
      const y = Number(label.getAttribute('y') || 0)
      const width = Number(label.getAttribute('width'))
      const text = document.createElementNS(ns, 'text')
      text.setAttribute('font-family', config.fontFamily || 'sans-serif')
      text.setAttribute('font-size', '14')
      // Preserve the label's computed foreground (some diagram types use fixed light boxes).
      text.setAttribute('fill', content ? getComputedStyle(content).color : 'currentColor')
      if (label.hasAttribute('transform')) text.setAttribute('transform', label.getAttribute('transform'))
      label.replaceWith(text)
      let row = 0
      for (const line of lines) {
        let span = document.createElementNS(ns, 'tspan')
        function append() {
          span.setAttribute('x', String(x))
          span.setAttribute('y', String(y + 14 + row++ * 17))
          text.append(span)
        }
        append()
        for (const word of line.split(/\\s+/)) {
          const before = span.textContent
          span.textContent = before ? before + ' ' + word : word
          if (before && width > 0 && span.getComputedTextLength() > width) {
            span.textContent = before
            span = document.createElementNS(ns, 'tspan')
            span.textContent = word
            append()
          }
        }
      }
    }
    return new XMLSerializer().serializeToString(root)
  }
  let serial = 0
  let port = null
  addEventListener('message', event => {
    if (port || event.source !== parent || !event.data || event.data.type !== 'port' || !event.ports[0]) return
    port = event.ports[0]
    port.onmessage = async message => {
      const data = message.data
      if (!data) return
      // BLG-468 §8: test-only — reads Mermaid's own parsed flowchart structure (its real
      // flowDb, the same object its own renderer draws from), for the differential gate to
      // diff our parser's AST against Mermaid's understanding of the same source, not just our
      // own corpus's expected output. Never called from any production code path; nothing here
      // crosses the port except plain, already-serializable data (ids/text/arrays of strings).
      if (data.type === 'inspect') {
        try {
          mermaid.initialize(data.config)
          const diagram = await mermaid.mermaidAPI.getDiagramFromText(data.source)
          const db = diagram.db
          if (typeof db.getActors === 'function') {
            // BLG-468 §8 (sequence): SequenceDB's own shape — actors (a Map), messages (an
            // array), the LINETYPE enum numbering each message's own type field (sent along
            // as-is so the test maps it back to a name without hardcoding Mermaid's own
            // numbering, which this differential test must stay robust to across a version bump).
            const actors = [...db.getActors().entries()].map(([id, a]) => ({ id, text: a.description ?? a.name ?? id, type: a.type }))
            const messages = db.getMessages().map(m => ({ from: m.from, to: m.to, text: m.message || null, type: m.type, activate: !!m.activate }))
            port.postMessage({ type: 'inspected', id: data.id, kind: 'sequence', actors, messages, lineType: db.LINETYPE })
            return
          }
          if (typeof db.getTasks === 'function') {
            // BLG-471 §8 (gantt): GanttDB's own shape — getTasks() resolves every task's
            // start/end (compileTasks(), including 'after' dependencies) before returning, the
            // same resolution its own renderer draws from, as a plain Date or a dayjs instance
            // depending on the resolution path taken — .valueOf() (epoch ms) is the one method
            // both share. Neither crosses postMessage's structured clone as itself — sent as ISO
            // strings instead, same discipline as every other field here (plain,
            // already-serializable data only).
            const tasks = db.getTasks().map(t => ({
              id: t.id, text: t.task, section: t.section,
              start: new Date(t.startTime.valueOf()).toISOString(), end: new Date(t.endTime.valueOf()).toISOString(),
              milestone: !!t.milestone, crit: !!t.crit, active: !!t.active, done: !!t.done,
            }))
            port.postMessage({ type: 'inspected', id: data.id, kind: 'gantt', tasks, sections: db.getSections() })
            return
          }
          const vertices = [...db.getVertices().values()].map(v => ({ id: v.id, text: v.text ?? v.id, type: v.type }))
          const edges = db.getEdges().map(e => ({ start: e.start, end: e.end, text: e.text || null }))
          const subGraphs = db.getSubGraphs().map(s => ({ id: s.id, title: s.title, nodes: s.nodes }))
          port.postMessage({ type: 'inspected', id: data.id, kind: 'flowchart', direction: db.getDirection?.() ?? null, vertices, edges, subGraphs })
        } catch (error) {
          port.postMessage({ type: 'error', id: data.id, message: String((error && error.message) || error) })
        }
        return
      }
      if (data.type !== 'render') return
      const host = document.createElement('div')
      scratch.append(host)
      try {
        mermaid.initialize(data.config)
        const { svg: drawn } = await mermaid.render('blerg-diagram-' + (++serial), data.source, host)
        const svg = data.normalizeTextLabels === false ? drawn : textLabels(drawn, host, data.config)
        // Measure in the frame when there is no viewBox to read on the main thread: a detached,
        // unlaid-out DOM there answers getBBox()/getBoundingClientRect() with 0. Only attached
        // long enough to measure; never kept in, or returned as, a live element.
        let measured = null
        const probeRoot = new DOMParser().parseFromString(svg, 'image/svg+xml').documentElement
        if (probeRoot.localName === 'svg' && !probeRoot.getAttribute('viewBox')) {
          const probe = document.createElement('div')
          probe.innerHTML = svg
          const probedSvg = probe.querySelector('svg')
          if (probedSvg) {
            host.append(probe)
            const box = probedSvg.getBBox ? probedSvg.getBBox() : probedSvg.getBoundingClientRect()
            if (box.width > 0 && box.height > 0) measured = { width: box.width, height: box.height }
            probe.remove()
          }
        }
        port.postMessage({ type: 'rendered', id: data.id, svg, measured })
      } catch (error) {
        port.postMessage({ type: 'error', id: data.id, message: String((error && error.message) || error) })
      } finally {
        host.remove()
      }
    }
    port.postMessage({ type: 'ready' })
  })
})()
`

function srcdoc(): string {
  // The bundle has no "</script" in it; escape it anyway so it can never end the element early.
  const code = mermaidScript.replace(/<\/(script)/gi, '<\\/$1').replace(/<!--/g, '<\\!--')
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${MERMAID_FRAME_CSP}"></head>`
    + `<body><div id="scratch"></div><script>${code}</script><script>${BOOT}</script></body></html>`
}

/** Mermaid's own bounding box, measured in the frame, only when the SVG has no `viewBox` to read
 *  on the main thread (mermaid.ts). */
export type MermaidMeasured = { width: number; height: number }
export type MermaidRendered = { svg: string; measured: MermaidMeasured | null }
/** The frame is the untrusted side (it ran Mermaid against attacker-influenced diagram source): a
 *  finite, positive check here, not just typeof — NaN, Infinity and non-positive sizes are all
 *  still "number" and would otherwise reach boundedSize()'s (mermaid.ts) division/Math.max
 *  unexamined. */
export function parseMeasured(raw: unknown): MermaidMeasured | null {
  const value = raw as { width?: unknown; height?: unknown } | null
  const finitePositive = (n: unknown): n is number => typeof n === 'number' && Number.isFinite(n) && n > 0
  return value && finitePositive(value.width) && finitePositive(value.height) ? { width: value.width, height: value.height } : null
}
/** `Record<string, unknown>` (not `MermaidRendered`): this one map now carries both the render
 *  and the test-only inspect jobs below, each narrowing the raw frame response its own way. */
type Pending = { resolve: (data: Record<string, unknown>) => void; reject: (error: Error) => void; timer: number }
type Frame = { iframe: HTMLIFrameElement; port: MessagePort; ready: Promise<void> }
let frame: Frame | null = null
const waiting = new Map<number, Pending>()
let nextId = 0
const timeouts = { start: 30_000, render: 60_000 }

/** For tests: shorter limits on starting the frame and on one render. */
export function setMermaidFrameTimeouts(next: Partial<typeof timeouts>): void {
  Object.assign(timeouts, next)
}

/** Drops the frame (a fresh one starts with the next render) and fails what it still owed. */
function discard(gone: Frame, reason: string) {
  if (frame !== gone) return
  frame = null
  gone.port.close()
  gone.iframe.remove()
  for (const [id, job] of waiting) {
    waiting.delete(id)
    window.clearTimeout(job.timer)
    job.reject(new Error(reason))
  }
}

function start(): Frame {
  if (frame?.iframe.isConnected) return frame
  if (frame) discard(frame, 'the diagram renderer was removed')
  const iframe = document.createElement('iframe')
  iframe.setAttribute('sandbox', 'allow-scripts')
  iframe.setAttribute('aria-hidden', 'true')
  iframe.setAttribute('tabindex', '-1')
  iframe.title = 'Diagram renderer'
  iframe.dataset.blergMermaidFrame = ''
  // Laid out (Mermaid measures text) but out of sight and out of reach.
  iframe.style.cssText = 'position:fixed;left:-100000px;top:0;width:1600px;height:1200px;border:0;visibility:hidden;pointer-events:none'
  iframe.srcdoc = srcdoc()
  // Requests and answers travel on a private channel, never on window messages: the port is
  // handed over once, when our own document has loaded. Any later load means the frame
  // navigated (its document is no longer ours): it is dropped, never reused.
  const channel = new MessageChannel()
  let loads = 0
  let started!: () => void
  let failed!: (error: Error) => void
  const ready = new Promise<void>((resolve, reject) => { started = resolve; failed = reject })
  const current: Frame = { iframe, port: channel.port1, ready }
  const timer = window.setTimeout(() => {
    failed(new Error('the diagram renderer did not start'))
    discard(current, 'the diagram renderer did not start')
  }, timeouts.start)
  iframe.addEventListener('load', () => {
    if (++loads > 1) { failed(new Error('the diagram renderer navigated away')); discard(current, 'the diagram renderer navigated away'); return }
    iframe.contentWindow?.postMessage({ type: 'port' }, '*', [channel.port2])
  })
  channel.port1.onmessage = event => {
    const data = event.data as { type?: string; id?: number; message?: unknown } & Record<string, unknown> | null
    if (!data || typeof data !== 'object') return
    if (data.type === 'ready') { window.clearTimeout(timer); started(); return }
    const job = typeof data.id === 'number' ? waiting.get(data.id) : undefined
    if (!job) return
    waiting.delete(data.id!)
    window.clearTimeout(job.timer)
    if (data.type === 'rendered' || data.type === 'inspected') job.resolve(data)
    else job.reject(new Error(typeof data.message === 'string' ? data.message : 'Mermaid could not draw it'))
  }
  ready.catch(() => {})
  frame = current
  document.body.append(iframe)
  return current
}

/** Draws inside the sandbox and returns unsanitized SVG, plus a frame-measured bounding box when
 *  the SVG has no viewBox to read on the main thread. Compatibility controls can request
 *  unmodified bundle output; production converts hardcoded XHTML labels to SVG text. */
export async function renderInFrame(source: string, config: Record<string, unknown>, normalizeTextLabels = true): Promise<MermaidRendered> {
  const current = start()
  await current.ready
  if (frame !== current) throw new Error('the diagram renderer was replaced')
  const id = ++nextId
  return new Promise<MermaidRendered>((resolve, reject) => {
    // A frame that does not answer in time is dropped, so the next render starts a fresh one.
    const timer = window.setTimeout(() => {
      waiting.delete(id)
      reject(new Error('the diagram took too long to draw'))
      discard(current, 'the diagram renderer was restarted')
    }, timeouts.render)
    waiting.set(id, { resolve: data => resolve({ svg: data.svg as string, measured: parseMeasured(data.measured) }), reject, timer })
    current.port.postMessage({ type: 'render', id, source, config, normalizeTextLabels })
  })
}

/** BLG-468 §8: test-only — Mermaid's own parsed flowchart structure (its real flowDb), read
 *  inside the same sandboxed frame, for the differential gate to diff our parser's AST against
 *  Mermaid's own understanding of the source rather than only our corpus's expected output.
 *  Never called from production: `render.ts`'s dispatcher only ever sends `{type: 'render'}`. */
export interface MermaidInspectedFlowchart {
  kind: 'flowchart'
  direction: string | null
  vertices: ReadonlyArray<{ id: string; text: string; type: string | undefined }>
  edges: ReadonlyArray<{ start: string; end: string; text: string | null }>
  subGraphs: ReadonlyArray<{ id: string; title: string; nodes: readonly string[] }>
}
export interface MermaidInspectedSequence {
  kind: 'sequence'
  actors: ReadonlyArray<{ id: string; text: string; type: string | undefined }>
  messages: ReadonlyArray<{ from: string; to: string; text: string | null; type: number; activate: boolean }>
  /** Mermaid's own `SequenceDB.LINETYPE` enum, as-is — §8's own sequence differential test maps
   *  a message's numeric `type` back to a name through this rather than a hardcoded copy, so a
   *  Mermaid version bump that renumbers it can't silently desync the two. */
  lineType: Record<string, number>
}
export interface MermaidInspectedGantt {
  kind: 'gantt'
  tasks: ReadonlyArray<{ id: string; text: string; section: string; start: string; end: string; milestone: boolean; crit: boolean; active: boolean; done: boolean }>
  sections: readonly string[]
}
export type MermaidInspected = MermaidInspectedFlowchart | MermaidInspectedSequence | MermaidInspectedGantt
export async function inspectInFrame(source: string, config: Record<string, unknown>): Promise<MermaidInspected> {
  const current = start()
  await current.ready
  if (frame !== current) throw new Error('the diagram renderer was replaced')
  const id = ++nextId
  return new Promise<MermaidInspected>((resolve, reject) => {
    const timer = window.setTimeout(() => {
      waiting.delete(id)
      reject(new Error('the diagram took too long to inspect'))
      discard(current, 'the diagram renderer was restarted')
    }, timeouts.render)
    waiting.set(id, {
      resolve: data => resolve(data as unknown as MermaidInspected),
      reject, timer,
    })
    current.port.postMessage({ type: 'inspect', id, source, config })
  })
}
