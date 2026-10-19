import { describe, expect, it } from 'vitest'
import { parseVegaLite, sanitizeSvg, validateMermaid } from './policy'

describe('visual policy', () => {
  it('accepts a bounded diagram and inline bar/line/scatter/heatmap charts', () => {
    expect(() => validateMermaid('flowchart LR\n A[Start] --> B[Done]')).not.toThrow()
    for (const mark of ['bar', 'line', 'point', 'rect']) {
      expect(parseVegaLite(JSON.stringify({data: {values: [{x: 1, y: 2}]}, mark,
        encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}}}))).toHaveProperty('mark', mark)
    }
  })
  it.each(['%%{init: {"securityLevel":"loose"}}%%\ngraph LR; A-->B',
    '---\nconfig:\n securityLevel: loose\n---\ngraph LR; A-->B',
    'graph LR; A-->B; click A "https://example.invalid"',
    'graph LR; A@{ img: "/private" }', 'graph LR; A["<img src=/private>"]',
    'graph LR; A["$$x^2$$"]', 'graph LR; classDef x fill:url(/private)', 'graph LR; A[https://example.invalid]'])('rejects unsafe Mermaid %s', source => {
    expect(() => validateMermaid(source)).toThrow()
  })
  it.each([{data: {url: '/private'}, mark: 'bar'},
    {data: {values: []}, mark: {type: 'image', url: '/private'}},
    {data: {values: []}, mark: 'point', encoding: {href: {value: '/private'}}},
    {data: {values: []}, mark: 'bar', transform: [{calculate: 'random()', as: 'x'}]},
    {data: {values: []}, mark: 'bar', layer: [{data: {url: '/private'}, mark: 'point'}]},
  ])('rejects Vega external inputs and custom expressions', value => {
    expect(() => parseVegaLite(JSON.stringify(value))).toThrow()
  })
  it.each([{bin: {step: 1e-100}}, {scale: {bins: {start: 0, stop: 1000, step: 1e-100}}}, {axis: {tickCount: 'millisecond'}}, {axis: {tickCount: {interval: 'second', step: 1}}}])('rejects generated bin and interval tick arrays', options => {
    expect(() => parseVegaLite(JSON.stringify({mark: 'bar', data: {values: [{x: 1}]}, encoding: {x: {field: 'x', type: 'quantitative', ...options}}}))).toThrow()
  })
  it('normalizes outer padding and refuses non-finite JSON numbers', () => {
    expect(parseVegaLite('{"mark":"bar","data":{"values":[]},"padding":1000000000}')).toHaveProperty('padding', 16)
    expect(() => parseVegaLite('{"mark":"bar","data":{"values":[{"x":1e309}]}}')).toThrow()
  })
  it('keeps the plot transparent without removing explicit author mark colors', () => {
    const spec = parseVegaLite(JSON.stringify({mark: {type: 'bar', color: '#123456'}, data: {values: []}, background: 'white'}))
    expect(spec.background).toBe('transparent')
    expect(spec.mark).toEqual({type: 'bar', color: '#123456'})
  })
  it('rejects active SVG and external references, preserves internal markers', () => {
    expect(sanitizeSvg('<svg xmlns="http://www.w3.org/2000/svg"><path marker-end="url(#arrow)" d="M0 0"/></svg>')).toContain('url(#arrow)')
    expect(() => sanitizeSvg('<svg xmlns="http://www.w3.org/2000/svg"><defs><filter id="shadow"><feDropShadow dx="0" dy="1" stdDeviation="2"/></filter><symbol id="label"><text>Safe</text></symbol></defs></svg>')).not.toThrow()
    for (const body of ['<script>alert(1)</script>', '<image href="/private"/>',
      '<g onclick="alert(1)"/>', '<foreignObject/>', '<style>@import "/private";</style>',
      '<path fill="url(https://example.invalid)"/>']) {
      expect(() => sanitizeSvg(`<svg xmlns="http://www.w3.org/2000/svg">${body}</svg>`)).toThrow()
    }
  })
})
