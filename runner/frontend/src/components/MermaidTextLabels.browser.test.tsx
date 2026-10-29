// Exercise the production iframe converter with controlled renderer output, including XML that
// requires HTML recovery. Real event-modeling output is covered separately by the corpus.
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { renderInFrame } from '../../../../packages/chat/src/visual/mermaidFrame'
import { renderMermaid } from '../../../../packages/chat/src/visual/mermaid'
import { sanitizeSvg } from '../../../../packages/chat/src/visual/policy'

const fixtures = (() => {
  const open = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 500 500"'
  const label = '<foreignObject width="160" height="450"><div xmlns="http://www.w3.org/1999/xhtml">'
    + 'Use &lt;b&gt;literal&lt;/b&gt; &amp; &quot;quotes&quot;<br>next&nbsp;line</div></foreignObject>'
  const hostile = {
    script: '<script>globalThis.unexpectedScript = true</script>',
    handler: '<g onload="globalThis.unexpectedHandler = true"/>',
    reference: '<image href="https://example.invalid/private"/>',
    xhtml: '<div xmlns="http://www.w3.org/1999/xhtml">outside the label</div>',
    active: '<animate attributeName="opacity" values="0;1" dur="1s"/>',
    css: '<style>rect{fill:url(/private)}</style>',
  }
  const outputs: Record<string, string> = {
    safe: `${open}>${label}</svg>`,
    rootHandler: `${open} onload="globalThis.unexpectedHandler = true">${label}</svg>`,
    textReference: `${open}>${label}<text href="/private">outside</text></svg>`,
  }
  for (const [name, sibling] of Object.entries(hostile)) {
    // Keep this XHTML sibling in an XML parse; an HTML parser moves a div outside the SVG.
    const data = name === 'xhtml' ? label.replace('<br>', '<br/>').replace('&nbsp;', '&#160;') : label
    outputs[name] = `${open}>${data}${sibling}</svg>`
  }
  return { outputs }
})()

// Only Mermaid's returned string is controlled. Replace the bundle script when this page
// appends its iframe; the actual srcdoc bootstrap, private channel, converter and parent run.
// A page-local hook avoids browser module mocks affecting concurrently loaded corpus files.
let restoreAppend: () => void
beforeAll(() => {
  const append = document.body.append.bind(document.body)
  const script = `const mermaid = {
    initialize() {},
    async render(_id, source) {
      return { svg: ${JSON.stringify(fixtures.outputs)}[source.trim().split('\\n').at(-1)] }
    }
  }`.replace(/<\/script/gi, '<\\/script')
  const spy = vi.spyOn(document.body, 'append').mockImplementation((...nodes) => {
    for (const node of nodes) {
      if (node instanceof HTMLIFrameElement && node.hasAttribute('data-blerg-mermaid-frame')) {
        node.srcdoc = node.srcdoc.replace(/<script>[\s\S]*?<\/script>/, () => `<script>${script}</script>`)
      }
    }
    append(...nodes)
  })
  restoreAppend = () => spy.mockRestore()
})
afterAll(() => {
  restoreAppend()
  document.querySelector('iframe[data-blerg-mermaid-frame]')?.remove()
})

const CONFIG = { startOnLoad: false, securityLevel: 'strict', htmlLabels: false }
const textOf = (doc: Document) => [...doc.querySelectorAll('tspan')].map(span => span.textContent).join(' ')

describe('XHTML label conversion and the parent SVG policy', () => {
  it('recovers HTML entities and breaks as complete, inert SVG text', async () => {
    // <br> and &nbsp; make the renderer string invalid XML, forcing HTML recovery.
    expect(new DOMParser().parseFromString(fixtures.outputs.safe, 'image/svg+xml').querySelector('parsererror')).not.toBeNull()
    const host = document.createElement('section')
    document.body.append(host)
    let dispose: (() => void) | undefined
    try {
      dispose = await renderMermaid('flowchart LR\nsafe', host)
      const image = host.querySelector('img')!
      expect(image.naturalWidth).toBeGreaterThan(0)
      const svg = new DOMParser().parseFromString(decodeURIComponent(image.src.split(',')[1]), 'image/svg+xml')
      expect(textOf(svg)).toBe('Use <b>literal</b> & "quotes" next line')
      expect([...svg.querySelectorAll('*')].map(el => el.localName)).toEqual(['svg', 'text', 'tspan', 'tspan', 'tspan'])
      for (const el of svg.querySelectorAll('*')) {
        expect(el.namespaceURI).toBe('http://www.w3.org/2000/svg')
        for (const attr of el.attributes) expect(attr.name).toMatch(/^(xmlns|viewBox|width|height|font-family|font-size|fill|x|y)$/)
      }
      for (const span of svg.querySelectorAll('tspan')) {
        expect(span.childNodes).toHaveLength(1)
        expect(span.firstChild!.nodeType).toBe(Node.TEXT_NODE)
      }
    } finally { dispose?.(); host.remove() }
  })

  it.each([
    ['script', 'Unsupported SVG element: script'],
    ['handler', 'Unsupported SVG attribute: onload'],
    ['reference', 'Unsupported SVG element: image'],
    ['xhtml', 'Unsupported SVG element: div'],
    ['active', 'Unsupported SVG element: animate'],
    ['css', 'Unsupported SVG styles'],
    ['rootHandler', 'Unsupported SVG attribute: onload'],
    ['textReference', 'Unsupported SVG attribute: href'],
  ])('converts the label but the parent still refuses %s', async (name, reason) => {
    const converted = await renderInFrame(`flowchart LR\n${name}`, CONFIG)
    const svg = new DOMParser().parseFromString(converted, 'image/svg+xml')
    expect(svg.querySelector('parsererror, foreignObject')).toBeNull()
    expect(textOf(svg)).toBe('Use <b>literal</b> & "quotes" next line')
    expect(() => sanitizeSvg(converted)).toThrow(reason)
    const host = document.createElement('section')
    await expect(renderMermaid(`flowchart LR\n${name}`, host)).rejects.toThrow(`the drawn diagram was refused (${reason})`)
    expect(host.children).toHaveLength(0)
  })
})
