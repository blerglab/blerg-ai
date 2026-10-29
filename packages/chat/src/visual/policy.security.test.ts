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

// BLG-329: every refusal names the key and where it is, and every nested spec gets the same rules.
describe('Vega-Lite refusals name the key and its path', () => {
  const values = [{ label: 'A', count: 1 }, { label: 'B', count: 2 }]
  const unit = { mark: 'bar', encoding: { x: { field: 'label', type: 'nominal' }, y: { field: 'count', type: 'quantitative' } } }
  const base = { data: { values }, ...unit }
  const refusal = (spec: unknown) => {
    try {
      parseVegaLite(typeof spec === 'string' ? spec : JSON.stringify(spec))
    } catch (error) {
      return (error as Error).message
    }
    throw new Error('the chart was accepted')
  }

  it.each([
    ['calculate', { ...base, transform: [{ calculate: 'datum.count * 2', as: 'double' }] }, '"transform" at $ (data transforms are not available yet)'],
    ['a string filter', { ...base, transform: [{ filter: 'datum.count > 1' }] }, '"transform" at $ (data transforms are not available yet)'],
    ['a string condition test', { ...base, encoding: { ...unit.encoding, color: { condition: { test: 'datum.count > 1', value: 'red' } } } }, '"test" at $.encoding.color.condition (expressions are disabled'],
    ['a nested string condition test', { ...base, encoding: { ...unit.encoding, color: { condition: { test: { and: [{ field: 'count', gt: 1 }, 'datum.count < 5'] }, value: 'red' }, value: 'blue' } } }, 'at $.encoding.color.condition.test.and[1]: expressions are disabled'],
    ['expr in encoding', { ...base, encoding: { ...unit.encoding, opacity: { value: { expr: 'random()' } } } }, '"expr" at $.encoding.opacity.value (expressions are disabled)'],
    ['an axis labelExpr', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'nominal', axis: { labelExpr: 'datum.label' } } } }, '"labelExpr" at $.encoding.x.axis (expressions are disabled)'],
    ['expr in config', { ...base, config: { axis: { labelColor: { expr: 'random()' } } } }, '"expr" at $.config.axis'],
    ['labelExpr in config', { ...base, config: { axis: { labelExpr: 'datum.value' } } }, '"labelExpr" at $.config.axis (expressions are disabled)'],
    ['a signal', { ...base, mark: { type: 'bar', color: { signal: 'color' } } }, '"signal" at $.mark.color (signals are disabled)'],
    ['params', { ...base, params: [{ name: 'pick', select: 'point' }] }, '"params" at $ (parameters and interaction are disabled)'],
    ['bind', { ...base, encoding: { ...unit.encoding, color: { value: 'red', bind: { input: 'color' } } } }, '"bind" at $.encoding.color (parameters and interaction are disabled)'],
    ['selection', { ...base, selection: { pick: { type: 'single' } } }, '"selection" at $ (parameters and interaction are disabled)'],
    ['a param condition', { ...base, encoding: { ...unit.encoding, color: { condition: { param: 'pick', value: 'red' } } } }, '"param" at $.encoding.color.condition'],
    ['data url', { ...base, data: { url: 'https://example.com/data.json' } }, '"url" at $.data (external resources are disabled)'],
    ['a nested data url', { layer: [base, { ...unit, data: { url: '/api/private' } }] }, '"url" at $.layer[1].data (external resources are disabled)'],
    ['a facet inner data url', { data: { values }, facet: { field: 'label' }, spec: { ...unit, data: { url: '/x' } } }, '"url" at $.spec.data'],
    ['config url', { ...base, config: { url: 'https://example.com' } }, '"url" at $.config (external resources are disabled)'],
    ['a config URL value', { ...base, config: { legend: { symbolType: 'https://example.com/a.svg' } } }, '"symbolType" at $.config.legend (config strings may not contain URLs or code)'],
    ['mark href', { ...base, mark: { type: 'point', href: 'javascript:alert(1)' } }, '"href" at $.mark (links are disabled)'],
    ['encoding href', { ...base, encoding: { ...unit.encoding, href: { field: 'label' } } }, '"href" at $.encoding (links are disabled)'],
    ['image url', { ...base, mark: { type: 'bar', url: 'https://example.com/a.png' } }, '"url" at $.mark (external resources are disabled)'],
    ['an image mark', { ...base, mark: 'image' }, '"mark" at $ (not a supported mark'],
    ['$ref', { ...base, $ref: 'https://example.com/spec.json' }, '"$ref" at $ (external references are disabled)'],
    ['customFormatTypes', { ...base, config: { customFormatTypes: true } }, '"customFormatTypes" at $.config (custom formatters are disabled)'],
    ['a custom formatType', { ...base, encoding: { ...unit.encoding, y: { field: 'count', type: 'quantitative', axis: { formatType: 'evil' } } } }, '"formatType" at $.encoding.y.axis (custom formatters are disabled)'],
    ['a locale object', { ...base, config: { locale: { number: { currency: ['$', ''] } } } }, '"locale" at $.config (locales are disabled)'],
    ['a font with url(', { ...base, config: { font: 'x; src: url(https://example.com/f.woff)' } }, '"font" at $.config (fonts are plain family names)'],
    ['a label font with url(', { ...base, config: { axis: { labelFont: 'url(https://example.com/f.woff)' } } }, '"labelFont" at $.config.axis (fonts are plain family names)'],
    ['a mark font with url(', { ...base, mark: { type: 'text', font: 'url(/x)' } }, '"font" at $.mark (fonts are plain family names)'],
    ['an unknown config key', { ...base, config: { numberFormat: '.2f' } }, '"numberFormat" at $.config (not an allowed config setting)'],
    ['a nested config', { hconcat: [{ ...base, config: { bar: { color: 'red' } } }] }, '"config" at $.hconcat[0] (config is allowed only at the top of the chart)'],
    ['a config condition', { ...base, config: { bar: { color: { condition: { test: 'true', value: 'red' } } } } }, '"color" at $.config.bar (only literal values are allowed in config)'],
    ['a lookup', { ...base, transform: [{ lookup: 'label', from: { data: { url: 'https://example.com/a.json' }, key: 'label' } }] }, '"transform" at $ (data transforms are not available yet)'],
    ['a nested transform', { layer: [{ ...base, transform: [{ aggregate: [{ op: 'count', as: 'n' }] }] }] }, '"transform" at $.layer[0] (data transforms are not available yet)'],
    ['spec outside facet or repeat', { ...base, spec: unit }, '"spec" at $ ("spec" is allowed only inside a facet or repeat)'],
    ['a misplaced layer', { ...base, encoding: { ...unit.encoding, layer: [unit] } }, '"layer" at $.encoding (not allowed here)'],
    ['more than 16 panels', { data: { values }, hconcat: Array.from({ length: 17 }, () => unit) }, '"hconcat" at $ (must be a list of at most 16 charts)'],
    ['a repeat over more than 16 panels', { data: { values }, repeat: { row: ['a', 'b', 'c', 'd', 'e'], column: ['a', 'b', 'c', 'd'] }, spec: unit }, '"repeat" at $ (too many panels (the limit is 16))'],
    ['more than 32 marks', { data: { values }, vconcat: Array.from({ length: 3 }, () => ({ hconcat: Array.from({ length: 11 }, () => unit) })) }, '"mark" at $.vconcat[2].hconcat[10] (too many marks (the limit is 32))'],
    ['marks multiplied by a repeat', { data: { values }, repeat: Array.from({ length: 16 }, (_, i) => `f${i}`), spec: { layer: [unit, unit, unit] } }, '(too many marks (the limit is 32))'],
    ['a prototype key', '{"data":{"values":[]},"mark":"bar","encoding":{"__proto__":{"x":1}}}', '"__proto__" at $.encoding (reserved object key)'],
    ['a prototype key in data', '{"data":{"values":[{"constructor":{"prototype":1}}]},"mark":"bar"}', '"constructor" at $.data.values[0] (reserved object key)'],
    ['the review DateTime year payload', { ...base, transform: [{ filter: { field: 'x', equal: { year: 'length(sequence(0,100000000))' } } }] }, '"transform" at $ (data transforms are not available yet)'],
    ['a DateTime year expression in a test', { ...base, encoding: { ...unit.encoding, color: { condition: { test: { field: 'x', equal: { year: 'length(sequence(0,100000000))' } }, value: 'red' }, value: 'blue' } } }, '"year" at $.encoding.color.condition.test.equal (must be a whole number from 0 to 9999)'],
    ['the review timed string payload', { ...base, transform: [{ filter: { field: 'x', timeUnit: 'year', equal: 'length(sequence(0,100000000))' } }] }, '"transform" at $ (data transforms are not available yet)'],
    ['a timed string operand in a test', { ...base, encoding: { ...unit.encoding, color: { condition: { test: { field: 'x', timeUnit: 'year', equal: 'length(sequence(0,100000000))' }, value: 'red' }, value: 'blue' } } }, '"equal" at $.encoding.color.condition.test (with a timeUnit, use numbers or date objects'],
    ['a timed string range bound', { ...base, encoding: { ...unit.encoding, color: { condition: { test: { field: 'x', timeUnit: 'month', range: ['length(sequence(0,1e8))', 3] }, value: 'red' }, value: 'blue' } } }, 'at $.encoding.color.condition.test.range[0]: with a timeUnit'],
    ['a timed string oneOf', { ...base, encoding: { ...unit.encoding, color: { condition: { test: { field: 'x', timeUnit: 'day', oneOf: [1, 'length(sequence(0,1e8))'] } , value: 'red' } } } }, 'at $.encoding.color.condition.test.oneOf[1]: with a timeUnit'],
    ['a DateTime month expression in a range', { ...base, encoding: { ...unit.encoding, color: { condition: { test: { field: 'x', range: [{ month: 'length(sequence(0,1e8))' }, null] }, value: 'red' }, value: 'blue' } } }, '"month" at $.encoding.color.condition.test.range[0] (must be a whole number from 1 to 12 or a month name)'],
    ['a DateTime datum expression', { ...base, encoding: { ...unit.encoding, x: { datum: { year: 2026, hours: 'length(sequence(0,1e8))' }, type: 'temporal' } } }, '"hours" at $.encoding.x.datum (must be a whole number from 0 to 23)'],
    ['a DateTime scale domain expression', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', scale: { domain: [{ year: 2020 }, { date: '1+1' }] } } } }, '"date" at $.encoding.x.scale.domain[1] (must be a whole number from 1 to 31)'],
    ['a DateTime with an extra key', { ...base, encoding: { ...unit.encoding, x: { datum: { year: 2026, expression: 1 }, type: 'temporal' } } }, '"expression" at $.encoding.x.datum (a date takes only year'],
    ['a fractional DateTime part', { ...base, encoding: { ...unit.encoding, x: { datum: { year: 2026.5 }, type: 'temporal' } } }, '"year" at $.encoding.x.datum (must be a whole number from 0 to 9999)'],
    ['a string utc flag', { ...base, encoding: { ...unit.encoding, x: { datum: { year: 2026, utc: 'yes' }, type: 'temporal' } } }, '"utc" at $.encoding.x.datum (a date takes utc as true or false)'],
    ['a timed string scale domain', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', timeUnit: 'year', scale: { domain: ['length(sequence(0,1e8))', 2026] } } } }, 'at $.encoding.x.scale.domain[0]: with a timeUnit, use numbers or date objects'],
    ['a timed string domainMax', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', timeUnit: 'month', scale: { domainMax: 'length(sequence(0,1e8))' } } } }, '"domainMax" at $.encoding.x.scale (with a timeUnit'],
    ['timed string axis values', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', timeUnit: 'month', axis: { values: ['length(sequence(0,1e8))'] } } } }, 'at $.encoding.x.axis.values[0]: with a timeUnit'],
    ['timed string legend values', { ...base, encoding: { ...unit.encoding, color: { field: 'label', type: 'temporal', timeUnit: 'month', legend: { values: ['x'] } } } }, 'at $.encoding.color.legend.values[0]: with a timeUnit'],
    ['axis values in config', { ...base, config: { axis: { values: [1, 2] } } }, '"values" at $.config.axis (not allowed in config)'],
    ['a flatten transform', { data: { values: [{ a: { length: 1e300 } }] }, mark: 'bar', transform: [{ flatten: ['a'] }] }, '"transform" at $ (data transforms are not available yet)'],
    ['twelve folds', { data: { values: [{ a: 1, b: 2 }] }, mark: 'bar', transform: Array.from({ length: 12 }, () => ({ fold: ['a', 'b'] })) }, '"transform" at $ (data transforms are not available yet)'],
    ['a transform inside a repeat', { data: { values: [{ a: 1 }] }, repeat: ['a'], spec: { mark: 'bar', transform: [{ fold: ['a'] }] } }, '"transform" at $.spec (data transforms are not available yet)'],
    ['the review timed sort payload', '{"data":{"values":[{"x":"2026-01-01"}]},"mark":"point","encoding":{"x":{"field":"x","type":"temporal","timeUnit":"year","sort":["length(sequence(0,100000000))"]}}}', 'at $.encoding.x.sort[0]: with a timeUnit, use numbers or date objects'],
    ['a non-ISO temporal sort string without a timeUnit', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', sort: ['2026-01-01', 'length(sequence(0,1e8))'] } } }, 'at $.encoding.x.sort[1]: a temporal field takes numbers, ISO dates'],
    ['a timed sort DateTime expression', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', timeUnit: 'month', sort: [{ month: 'x)+(1' }] } } }, '"month" at $.encoding.x.sort[0] (must be a whole number from 1 to 12 or a month name)'],
    ['the review bandPosition payload', '{"data":{"values":[{"x":1,"y":1,"g":"a"}]},"mark":"area","encoding":{"x":{"field":"x","type":"quantitative","bin":true,"bandPosition":"length(sequence(0,100000000))"},"y":{"field":"y","type":"quantitative","aggregate":"sum"},"color":{"field":"g","type":"nominal"}}}', '"bandPosition" at $.encoding.x (must be a number from 0 to 1)'],
    ['a bandPosition out of range', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'nominal', bandPosition: 2 } } }, '"bandPosition" at $.encoding.x (must be a number from 0 to 1)'],
    ['a bandPosition in config', { ...base, config: { bar: { bandPosition: '1+1' } } }, '"bandPosition" at $.config.bar (must be a number from 0 to 1)'],
    ['a string binSpacing', { ...base, mark: { type: 'bar', binSpacing: 'length(sequence(0,1e8))' } }, '"binSpacing" at $.mark (must be a number, not text)'],
    ['a string cornerRadius', { ...base, mark: { type: 'bar', cornerRadiusEnd: '1+1' } }, '"cornerRadiusEnd" at $.mark (must be a number, not text)'],
    ['a string opacity', { ...base, mark: { type: 'bar', opacity: '1+1' } }, '"opacity" at $.mark ("1+1" is not one of its allowed values)'],
    ['a string strokeWidth in config', { ...base, config: { line: { strokeWidth: '1+1' } } }, '"strokeWidth" at $.config.line ("1+1" is not one of its allowed values)'],
    ['a string angle', { ...base, mark: { type: 'text', angle: '1+1' } }, '"angle" at $.mark ("1+1" is not one of its allowed values)'],
    ['a string scale padding', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'nominal', scale: { paddingInner: '1+1' } } } }, '"paddingInner" at $.encoding.x.scale (must be a number, not text)'],
    ['an unknown scale nice interval', { ...base, encoding: { ...unit.encoding, y: { field: 'count', type: 'quantitative', scale: { nice: 'length(sequence(0,1e8))' } } } }, '"nice" at $.encoding.y.scale ("length(sequence(0,1e8))" is not one of its allowed values)'],
    ['a string scale zero', { ...base, encoding: { ...unit.encoding, y: { field: 'count', type: 'quantitative', scale: { zero: 'true' } } } }, '"zero" at $.encoding.y.scale (must be a number, not text)'],
    ['an unknown stack offset', { ...base, encoding: { ...unit.encoding, y: { field: 'count', type: 'quantitative', stack: 'count' } } }, '"stack" at $.encoding.y ("count" is not one of its allowed values)'],
    ['a string strokeDash entry', { ...base, mark: { type: 'line', strokeDash: [2, '1+1'] } }, '"strokeDash" at $.mark ("1+1" is not one of its allowed values)'],
    ['the review impute payload', '{"data":{"values":[{"x":0,"y":1}]},"mark":"line","encoding":{"x":{"field":"x","type":"quantitative"},"y":{"field":"y","type":"quantitative","impute":{"keyvals":{"start":0,"stop":100000000,"step":1}}}}}', '"impute" at $.encoding.y (imputation is not available)'],
    ['a prototype field', '{"data":{"values":[{"x":1}]},"mark":"bar","encoding":{"y":{"field":"__proto__","type":"quantitative","aggregate":"sum"}}}', '"field" at $.encoding.y (reserved field name)'],
    ['a nested prototype field path', { ...base, encoding: { ...unit.encoding, y: { field: 'a.constructor.prototype', type: 'quantitative', aggregate: 'sum' } } }, '"field" at $.encoding.y (reserved field name)'],
    ['an escaped prototype field path', { ...base, encoding: { ...unit.encoding, y: { field: 'a\\.__proto__', type: 'quantitative' } } }, '"field" at $.encoding.y (reserved field name)'],
    ['a bracketed prototype field path', { ...base, encoding: { ...unit.encoding, y: { field: 'a["__proto__"]', type: 'quantitative' } } }, '"field" at $.encoding.y (reserved field name)'],
    ['a prototype sort field', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'nominal', sort: { field: '__proto__', op: 'sum' } } } }, '"field" at $.encoding.x.sort (reserved field name)'],
    ['a prototype field in a test', { ...base, encoding: { ...unit.encoding, color: { condition: { test: { field: 'constructor', equal: 1 }, value: 'red' }, value: 'blue' } } }, '"field" at $.encoding.color.condition.test (reserved field name)'],
    ['a prototype facet field', { data: { values }, facet: { row: { field: '__proto__' } }, spec: unit }, '"field" at $.facet.row (reserved field name)'],
    ['a prototype repeat field', { data: { values }, repeat: ['count', 'prototype'], spec: unit }, '"repeat" at $ (reserved field name)'],
    ['a prototype argmax field', { ...base, encoding: { ...unit.encoding, y: { field: 'count', type: 'quantitative', aggregate: { argmax: '__proto__' } } } }, '"argmax" at $.encoding.y.aggregate (reserved field name)'],
    ['a prototype tooltip field', { ...base, encoding: { ...unit.encoding, tooltip: [{ field: '__proto__' }] } }, '"field" at $.encoding.tooltip[0] (reserved field name)'],
    ['a stacked area that fills in 25 million points', { data: { values: Array.from({ length: 5000 }, (_, i) => ({ x: i, g: i })) }, mark: 'area', encoding: { x: { field: 'g', type: 'quantitative' }, y: { field: 'x', type: 'quantitative', aggregate: 'sum' }, color: { field: 'x', type: 'nominal' } } }, '"color" at $.encoding (too many points to fill in: 5,000 keys × 5,000 groups could make 25,000,000 points (the limit is 100,000))'],
    ['a stacked area layer with inherited data and encoding', { data: { values: Array.from({ length: 5000 }, (_, i) => ({ x: i, g: i })) }, encoding: { x: { field: 'g', type: 'quantitative' }, color: { field: 'x', type: 'nominal' } }, layer: [{ mark: 'area', encoding: { y: { field: 'x', type: 'quantitative', aggregate: 'sum' } } }] }, '"color" at $.layer[0].encoding (too many points to fill in'],
    ['an explicitly stacked line from a named dataset', { datasets: { d: Array.from({ length: 2000 }, (_, i) => ({ x: i, g: i })) }, data: { name: 'd' }, mark: 'line', encoding: { x: { field: 'x', type: 'quantitative' }, y: { field: 'x', type: 'quantitative', stack: 'zero' }, detail: { field: 'g' } } }, '"detail" at $.encoding (too many points to fill in'],
    ['a huge format width', { ...base, encoding: { ...unit.encoding, y: { field: 'count', type: 'quantitative', axis: { format: '100000000.0f' } } } }, '"format" at $.encoding.y.axis (format widths and precisions must be at most 100)'],
    ['a huge format precision in a tooltip', { ...base, encoding: { ...unit.encoding, tooltip: { field: 'count', type: 'quantitative', format: '.101f' } } }, '"format" at $.encoding.tooltip (format widths and precisions must be at most 100)'],
    ['a huge config format', { ...base, config: { axis: { format: ',.1000f' } } }, '"format" at $.config.axis (format widths and precisions must be at most 100)'],
    ['a huge text format', { ...base, mark: 'text', encoding: { ...unit.encoding, text: { field: 'count', type: 'quantitative', format: '999e' } } }, '"format" at $.encoding.text (format widths and precisions must be at most 100)'],
    ['a non-ISO string in a temporal domain', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', scale: { domain: ['2026-01-01', 'length(sequence(0,1e8))'] } } } }, 'at $.encoding.x.scale.domain[1]: a temporal field takes numbers, ISO dates'],
    ['a non-ISO string in temporal axis values', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', axis: { values: ['soon'] } } } }, 'at $.encoding.x.axis.values[0]: a temporal field takes numbers, ISO dates'],
    ['a weekday of 7', { ...base, encoding: { ...unit.encoding, x: { datum: { day: 7 }, type: 'temporal' } } }, '"day" at $.encoding.x.datum (must be a whole number from 0 to 6 or a weekday name)'],
    ['a stacked area grouped through an array path', { data: { values: Array.from({ length: 400 }, (_, i) => ({ x: i, y: 1, g: [i] })) }, mark: 'area', encoding: { x: { field: 'x', type: 'quantitative' }, y: { field: 'y', type: 'quantitative' }, color: { field: 'g.0', type: 'nominal' } } }, '"color" at $.encoding (too many points to fill in: 400 keys × 400 groups'],
    ['a facet over 100 values', { data: { values: Array.from({ length: 100 }, (_, i) => ({ k: i, count: i })) }, facet: { field: 'k', type: 'nominal' }, spec: unit }, '"facet" at $ (too many panels: 100 (the limit is 64))'],
    ['a facet channel over 100 values', { data: { values: Array.from({ length: 100 }, (_, i) => ({ k: i, count: i })) }, ...unit, encoding: { ...unit.encoding, row: { field: 'k' } } }, '"encoding" at $ (too many panels: 100 (the limit is 64))'],
    ['a row x column facet over 9 x 9', { data: { values: Array.from({ length: 81 }, (_, i) => ({ r: i % 9, c: Math.floor(i / 9) })) }, facet: { row: { field: 'r' }, column: { field: 'c' } }, spec: unit }, '"facet" at $ (too many panels: 81'],
    ['concatenated facets over 64 panels', { data: { values: Array.from({ length: 40 }, (_, i) => ({ k: i, j: i % 30, count: i })) }, hconcat: [{ facet: { field: 'k' }, spec: unit }, { facet: { field: 'j' }, spec: unit }] }, '"hconcat" at $ (too many panels: 70 (the limit is 64))'],
    ['a facet of repeated charts', { data: { values: Array.from({ length: 10 }, (_, i) => ({ k: i, count: i })) }, facet: { field: 'k' }, spec: { repeat: ['count', 'label', 'k', 'count', 'label', 'k', 'count'], spec: unit } }, '"facet" at $ (too many panels: 70'],
    ['a horizontal area whose key cannot be proven', { data: { values: Array.from({ length: 400 }, (_, i) => ({ x: 0, y: i, c: i })) }, mark: { type: 'area', orient: 'horizontal' }, encoding: { x: { field: 'x', type: 'quantitative' }, y: { field: 'y', type: 'quantitative' }, color: { field: 'c', type: 'nominal' } } }, '"color" at $.encoding (too many points to fill in: 400 keys × 400 groups'],
    ['an area with orient in config whose key cannot be proven', { data: { values: Array.from({ length: 400 }, (_, i) => ({ x: 0, y: i, c: i })) }, mark: 'area', config: { area: { orient: 'horizontal' } }, encoding: { x: { field: 'x', type: 'quantitative' }, y: { field: 'y', type: 'quantitative' }, color: { field: 'c', type: 'nominal' } } }, '"color" at $.encoding (too many points to fill in: 400 keys × 400 groups'],
    ['the same ordinal-key area counted by the larger channel once its x is binned', { data: { values: Array.from({ length: 3000 }, (_, i) => ({ k: i % 10, v: i, t: i % 50 })) }, mark: 'area', encoding: { x: { field: 'k', type: 'quantitative', bin: true }, y: { field: 'v', type: 'quantitative' }, color: { field: 't', type: 'nominal' } } }, '"color" at $.encoding (too many points to fill in: 3,000 keys × 50 groups'],
    ['a number formatType on a temporal axis', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', axis: { formatType: 'number', format: '1000000000f' } } } }, '"format" at $.encoding.x.axis (format widths and precisions must be at most 100)'],
    ['a number formatType on a temporal field', { ...base, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', formatType: 'number', axis: { format: '1000000000f' } } } }, '"format" at $.encoding.x.axis (format widths and precisions must be at most 100)'],
    ['a number formatType in config with a temporal axis', { ...base, config: { axisTemporal: { formatType: 'number' } }, encoding: { ...unit.encoding, x: { field: 'label', type: 'temporal', axis: { format: '1000000000f' } } } }, '"format" at $.encoding.x.axis (format widths and precisions must be at most 100)'],
    ['an unknown dataset name', { datasets: { a: values }, data: { name: 'b' }, ...unit }, '"name" at $.data (data.name must name an entry in the top-level datasets)'],
  ])('refuses %s', (_name, spec, message) => {
    expect(refusal(spec)).toContain(message)
  })

  it('never lets a prototype field reach Object.prototype through the render path', async () => {
    const [{ compile }, { View, parse, logger }, { expressionInterpreter }, { denyVisualResources }] = await Promise.all([
      import('vega-lite'), import('vega'), import('vega-interpreter'), import('./vega')])
    const draw = async (source: string) => {
      const compiled = compile(parseVegaLite(source) as never, { logger: logger(0) }).spec
      const view = new View(parse(compiled, undefined, { ast: true }), { renderer: 'none', loader: denyVisualResources, logger: logger(0), expr: expressionInterpreter })
      try { await view.runAsync() } finally { view.finalize() }
    }
    for (const field of ['__proto__', 'constructor.prototype', 'a\\.__proto__']) {
      const source = `{"data":{"values":[{"x":1}]},"mark":"bar","encoding":{"y":{"field":${JSON.stringify(field)},"type":"quantitative","aggregate":"sum"}}}`
      await expect(draw(source)).rejects.toThrow(/reserved field name/)
    }
    expect(Object.hasOwn(Object.prototype, 'sum')).toBe(false)
    // The same chart over an ordinary field draws, and still leaves the prototype alone.
    await draw('{"data":{"values":[{"x":1}]},"mark":"bar","encoding":{"y":{"field":"x","type":"quantitative","aggregate":"sum"}}}')
    expect(Object.hasOwn(Object.prototype, 'sum')).toBe(false)
  })

  it.each(['.2f', ',.0f', '%Y-%m', '~s', '$,.2f', '.100f'])('accepts the ordinary format %s', format => {
    expect(() => parseVegaLite(JSON.stringify({ ...base, encoding: { ...unit.encoding, y: { field: 'count', type: 'quantitative', axis: { format } } } }))).not.toThrow()
  })

  it.each([
    ['a temporal axis format', { field: 'label', type: 'temporal', axis: { format: '%Y' } }],
    ['a literal year in a time format', { field: 'label', type: 'temporal', axis: { format: '%b 2026' } }],
    ['a time format on a timed field', { field: 'label', type: 'ordinal', timeUnit: 'yearmonth', format: '%b 2026' }],
    ['a formatType time', { field: 'label', type: 'quantitative', format: '%d.%m.2026', formatType: 'time' }],
  ])('accepts %s', (_name, x) => {
    expect(() => parseVegaLite(JSON.stringify({ ...base, encoding: { ...unit.encoding, x } }))).not.toThrow()
  })

  it('caps a format by what d3-format would parse, whatever the field or formatType', () => {
    // A time format is never a number specifier, so it is not capped, even in quantitative config
    // (where d3-format would simply refuse it as invalid).
    expect(() => parseVegaLite(JSON.stringify({ ...base, config: { axisTemporal: { format: '%b 2026' } } }))).not.toThrow()
    expect(() => parseVegaLite(JSON.stringify({ ...base, config: { axisQuantitative: { format: '%b 2026' } } }))).not.toThrow()
    // A quantitative field formats as a number even with formatType time (Vega-Lite's formatSignalRef).
    expect(refusal({ data: { values: [{ v: 1 }] }, mark: 'text', encoding: { text: { field: 'v', type: 'quantitative', formatType: 'time', format: '1000000000f' } } }))
      .toContain('"format" at $.encoding.text (format widths and precisions must be at most 100)')
    expect(refusal({ ...base, config: { axisQuantitative: { format: '.1000%' } } })).toContain('"format" at $.config.axisQuantitative (format widths and precisions must be at most 100)')
  })

  it.each([
    ['ISO dates in a temporal domain, sort and axis values', { field: 'label', type: 'temporal', scale: { domain: ['2026-01-01', '2026-12-31T23:59:59Z'] }, sort: ['2026-01'], axis: { values: ['2026-06-01 12:00', 2026] } }],
    ['a Sunday as day 0', { datum: { day: 0 }, type: 'temporal' }],
  ])('accepts %s', (_name, x) => {
    expect(() => parseVegaLite(JSON.stringify({ ...base, encoding: { ...unit.encoding, x } }))).not.toThrow()
  })

  it('refuses deep nesting by naming where it stopped', () => {
    const source = '{"layer":['.repeat(30) + JSON.stringify(base) + ']}'.repeat(30)
    expect(refusal(source)).toMatch(/^Chart refused at \$(?:\.layer\[0\])+.*: the chart is nested too deeply/)
    const deepPredicate = { ...base, encoding: { ...unit.encoding, color: { condition: { test: JSON.parse('{"not":'.repeat(60) + '{"field":"count","gt":1}' + '}'.repeat(60)), value: 'red' } } } }
    expect(refusal(deepPredicate)).toContain('nested too deeply')
  })

  it('keeps refusal messages short and free of renderer internals', () => {
    const message = refusal({ ...base, encoding: { ...unit.encoding, ['x'.repeat(500) + 'Expr']: 'datum.x' } })
    expect(message.length).toBeLessThan(300)
    expect(message).toMatch(/^Chart refused: "x{39}…" at \$\.encoding \(expressions are disabled\)$/)
    const nested = refusal({ ...base, encoding: { ...unit.encoding, ['y'.repeat(500)]: { expr: 'x' } } })
    expect(nested).toMatch(/^Chart refused: "expr" at \$\.encoding\.y{39}… \(expressions are disabled\)$/)
  })
})
