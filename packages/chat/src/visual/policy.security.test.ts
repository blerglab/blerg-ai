import { describe, expect, it } from 'vitest'
import { parseVegaLite, sanitizeSvg, validateMermaid } from './policy'

describe('independent adversarial visual-source policy', () => {
  it.each([
    ['flowchart', 'flowchart LR\n  A[Start] --> B[Finish]'],
    ['sequence', 'sequenceDiagram\n  Alice->>Bob: Hello'],
    ['state', 'stateDiagram-v2\n  [*] --> Ready\n  Ready --> [*]'],
    ['ER', 'erDiagram\n  ACCOUNT ||--o{ SESSION : owns'],
    ['gantt', 'gantt\n  title Work\n  dateFormat YYYY-MM-DD\n  section Build\n  Review :2026-01-01, 1d'],
  ])('keeps the required %s diagram family', (_name, source) => {
    expect(() => validateMermaid(source)).not.toThrow()
  })

  it.each([
    ['initialization directive', '%%{init: {"securityLevel":"loose"}}%%\nflowchart LR\n A-->B'],
    ['configuration directive', '%%{config: {"themeCSS":"body{display:none}"}}%%\nflowchart LR\n A-->B'],
    ['frontmatter config', '---\nconfig:\n  securityLevel: loose\n---\nflowchart LR\n A-->B'],
    ['frontmatter theme CSS', '---\nconfig:\n  themeCSS: "@import url(https://example.com/style)"\n---\nflowchart LR\n A-->B'],
    ['click callback', 'flowchart LR\n A-->B\n click A callback'],
    ['click URL', 'flowchart LR\n A-->B\n click A href "https://example.com/track"'],
    ['sequence link', 'sequenceDiagram\n Alice->>Bob: Hi\n link Alice: Open @ https://example.com/track'],
    ['new image shape', 'flowchart LR\n A@{ img: "https://example.com/track.png", label: "A" }'],
    ['image HTML', 'flowchart LR\n A["<img src=https://example.com/track>"]'],
    ['foreign HTML', 'flowchart LR\n A["<iframe src=https://example.com/track></iframe>"]'],
    ['script label', 'flowchart LR\n A["<script>alert(1)</script>"]'],
    ['style URL', 'flowchart LR\n A-->B\n style A fill:url(https://example.com/track)'],
    ['class CSS', 'flowchart LR\n A:::attack-->B\n classDef attack fill:url(https://example.com/track)'],
    ['link CSS', 'flowchart LR\n A-->B\n linkStyle 0 stroke:url(https://example.com/track)'],
    ['CSS escape', 'flowchart LR\n A-->B\n classDef attack fill:u\\72l(https://example.com/track)'],
    ['markdown image', 'flowchart LR\n A["`![image](https://example.com/track)`"]'],
    ['protocol-relative markdown image', 'flowchart LR\n A["`![x](//example.com/track)`"]'],
    ['relative markdown image', 'flowchart LR\n A["`![x](/api/private)`"]'],
    ['out-of-scope math rendering', 'flowchart LR\n A["$$\\href{//example.com/track}{x}$$"]'],
    ['protocol-relative image', 'flowchart LR\n A@{ img: "//example.com/track.png" }'],
    ['data image', 'flowchart LR\n A@{ img: "data:image/svg+xml,<svg onload=alert(1)>" }'],
  ])('refuses Mermaid %s before any rendering side effect', (_name, source) => {
    expect(() => validateMermaid(source)).toThrow()
  })

  const valid = {
    data: { values: [{ label: 'A', count: 1 }, { label: 'B', count: 2 }] },
    mark: { type: 'bar', tooltip: true },
    encoding: { x: { field: 'label', type: 'nominal' }, y: { field: 'count', type: 'quantitative' } },
  }

  it('keeps ordinary inline-data charts', () => {
    expect(parseVegaLite(JSON.stringify(valid))).toMatchObject(valid)
  })

  it.each([
    ['remote data', { ...valid, data: { url: 'https://example.com/track.json' } }],
    ['local data', { ...valid, data: { url: '/api/private' } }],
    ['protocol-relative data', { ...valid, data: { url: '//example.com/track' } }],
    ['data URL', { ...valid, data: { url: 'data:application/json,[1]' } }],
    ['nested layer source', { layer: [valid, { ...valid, data: { url: 'https://example.com/track' } }] }],
    ['nested concat source', { hconcat: [valid, { ...valid, data: { url: 'https://example.com/track' } }] }],
    ['facet source', { ...valid, facet: { field: 'label' }, spec: { ...valid, data: { url: 'https://example.com/track' } } }],
    ['lookup source', { ...valid, transform: [{ lookup: 'label', from: { data: { url: 'https://example.com/track' }, key: 'label' } }] }],
    ['image mark', { ...valid, mark: 'image', encoding: { url: { value: 'https://example.com/track.png' } } }],
    ['image object mark', { ...valid, mark: { type: 'image', url: 'https://example.com/track.png' } }],
    ['mark hyperlink', { ...valid, mark: { type: 'point', href: 'javascript:alert(1)' } }],
    ['encoded hyperlink', { ...valid, encoding: { href: { field: 'label' } } }],
    ['calculated field', { ...valid, transform: [{ calculate: "'https://example.com/' + datum.label", as: 'link' }] }],
    ['filter expression', { ...valid, transform: [{ filter: 'sequence(0, 1000000000)' }] }],
    ['parameter expression', { ...valid, params: [{ name: 'x', expr: 'sequence(0, 1000000000)' }] }],
    ['external DOM binding target', { ...valid, params: [{ name: 'x', value: 'A', bind: { input: 'text', element: '#application' } }] }],
    ['signal reference', { ...valid, width: { signal: 'width' } }],
    ['external schema reference', { ...valid, $ref: 'https://example.com/spec.json' }],
    ['enormous stepped width', { ...valid, width: { step: 1_000_000_000 } }],
    ['enormous stepped height', { ...valid, height: { step: 1_000_000_000 } }],
    ['enormous density output', { ...valid, transform: [{ density: 'count', steps: 1_000_000_000 }] }],
    ['pathological bin step', { ...valid, encoding: { x: { field: 'count', type: 'quantitative', bin: { step: 1e-100 } } } }],
    ['generated scale bins', { ...valid, encoding: { x: { field: 'count', type: 'quantitative', scale: { bins: { start: 0, stop: 1, step: 1e-100 } } } } }],
    ['generated axis ticks', { ...valid, encoding: { x: { field: 'count', type: 'quantitative', axis: { tickCount: { interval: 'millisecond', step: 1e-100 } } } } }],
    ['prototype mutation', JSON.parse('{"data":{"values":[]},"mark":"bar","__proto__":{"polluted":true}}')],
  ])('refuses Vega-Lite %s without invoking a loader', (_name, spec) => {
    expect(() => parseVegaLite(JSON.stringify(spec))).toThrow()
  })

  it.each(['null', '[]', '1', '"chart"'])('rejects non-object chart source %s', source => {
    expect(() => parseVegaLite(source)).toThrow()
  })

  it('refuses pathological nesting with an ordinary error', () => {
    const source = '{"layer":['.repeat(200) + JSON.stringify(valid) + ']}'.repeat(200)
    expect(() => parseVegaLite(source)).toThrow()
  })

  it('refuses non-finite numbers created by JSON numeric overflow', () => {
    expect(() => parseVegaLite(JSON.stringify(valid).replace('"count":1', '"count":1e309'))).toThrow()
  })

  it.each([1_000_000_000, { top: 1_000_000_000, bottom: 0, left: 0, right: 0 }])('bounds padding before canvas initialization', padding => {
    let parsed: Record<string, unknown>
    try {
      parsed = parseVegaLite(JSON.stringify({ ...valid, padding }))
    } catch (error) {
      expect(error).toBeInstanceOf(Error)
      return
    }
    for (const value of typeof parsed.padding === 'object' && parsed.padding !== null ? Object.values(parsed.padding) : [parsed.padding]) {
      expect(typeof value).toBe('number')
      expect(value as number).toBeLessThanOrEqual(128)
      expect(value as number).toBeGreaterThanOrEqual(0)
    }
  })
})

describe('independent adversarial SVG sanitization', () => {
  it.each([
    ['script', '<script>alert(1)</script>'],
    ['foreign object', '<foreignObject><iframe xmlns="http://www.w3.org/1999/xhtml" src="https://example.com/track"/></foreignObject>'],
    ['remote image', '<image href="https://example.com/track.png" width="10" height="10"/>'],
    ['xlink image', '<image xmlns:xlink="http://www.w3.org/1999/xlink" xlink:href="https://example.com/track.png"/>'],
    ['remote use', '<use href="https://example.com/track.svg#shape"/>'],
    ['filter image resource', '<filter><feImage href="https://example.com/track.png"/></filter>'],
    ['javascript anchor', '<a href="java&#x73;cript:alert(1)"><text>Open</text></a>'],
    ['CSS import', '<style>@import url(https://example.com/track.css);</style>'],
    ['CSS escaped URL', '<style>.node{fill:u\\72l(https://example.com/track)}</style>'],
    ['attribute style URL', '<rect style="fill:url(https://example.com/track)" width="10" height="10"/>'],
    ['paint URL', '<rect fill="url(https://example.com/track)" width="10" height="10"/>'],
    ['event handler', '<g onload="alert(1)"><rect width="10" height="10"/></g>'],
    ['animation mutation', '<set attributeName="href" to="https://example.com/track"/>'],
  ])('removes active %s from rendered SVG', (_name, attack) => {
    let sanitized: string
    try {
      sanitized = sanitizeSvg(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20">${attack}<text x="1" y="10">safe label</text></svg>`)
    } catch (error) {
      // Rejecting the entire renderer output is equally safe; the component
      // then displays the original code without mounting an SVG document.
      expect(error).toBeInstanceOf(Error)
      return
    }
    const document = new DOMParser().parseFromString(sanitized, 'image/svg+xml')
    expect(document.querySelector('parsererror')).toBeNull()
    expect(document.querySelector('script,foreignObject,image,style,set,iframe')).toBeNull()
    for (const element of document.querySelectorAll('*')) {
      for (const attribute of element.attributes) {
        expect(attribute.name).not.toMatch(/^on/i)
        expect(attribute.value).not.toMatch(/example\.com|javascript:|u\\72l/i)
      }
    }
    expect(document.documentElement.textContent).toContain('safe label')
  })
})
