import { describe, expect, it } from 'vitest'
import { normalizeMermaid, parseVegaLite, sanitizeSvg, validateMermaid } from './policy'
import { CLASSDEF_AND_CLASS, IMAGE_WORD_LABEL, serviceGraph } from './mermaidSamples'

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

// Board card #84: real diagrams were refused for words in their labels and for color styling.
describe('Mermaid policy on real diagrams', () => {
  it('accepts a label that contains the word "image"', () => {
    expect(() => validateMermaid(IMAGE_WORD_LABEL)).not.toThrow()
    expect(() => validateMermaid('flowchart LR\n  a[icon set] --> b[img tag docs] --> c[Style guide] --> d[click here]')).not.toThrow()
  })
  it('accepts classDef plus class, style and linkStyle with safe properties', () => {
    expect(() => validateMermaid(CLASSDEF_AND_CLASS)).not.toThrow()
    expect(() => validateMermaid('flowchart LR\n  A:::hot --> B\n  classDef hot fill:#f96;\n  style B fill:#bbf,stroke:#f66,stroke-width:2px,color:#fff,stroke-dasharray: 5 5\n  linkStyle default interpolate basis stroke:#333,opacity:0.5')).not.toThrow()
  })
  it('accepts a forty-node service graph with several classDefs and a title', () => {
    const graph = serviceGraph()
    expect((graph.match(/\[/g) ?? []).length).toBeGreaterThanOrEqual(40)
    expect(() => validateMermaid(graph)).not.toThrow()
  })
  it('accepts class annotations and line breaks, which are not HTML', () => {
    expect(() => validateMermaid('classDiagram\n  class Shape\n  <<interface>> Shape\n  Shape <|-- Circle')).not.toThrow()
    expect(() => validateMermaid('flowchart LR\n  A[one<br/>two] --> B[three<br>four]')).not.toThrow()
  })
  it.each([
    ['flowchart LR\n  A --> B\n  classDef x fill:url(#a)', 'line 3: url() not allowed'],
    ['flowchart LR\n  A --> B\n  style A background:red', 'line 3: the style property "background" not allowed'],
    ['flowchart LR\n  A --> B\n  classDef x fill:red !important', 'line 3: the fill value "red !important" not allowed'],
    ['flowchart LR\n  A --> B\n  classDef x fill:u\\72l(x)', 'line 3: the fill value "u\\72l(x)" not allowed'],
    ['flowchart LR\n  A --> B\n  A-->C; style C fill:var(--x)', 'line 3: the fill value "var(--x)" not allowed'],
    ['flowchart LR\n  A@{ img: "/x.png" } --> B', 'line 2: the @{…} key "img" not allowed'],
    ['flowchart LR\n  A --> B\n  B --> C["<img src=x>"]', 'line 3: HTML not allowed'],
    ['flowchart LR\n  A --> B\n  click A callback', 'line 3: a click action not allowed'],
    ['flowchart LR\n  A --> B["see https://example.invalid"]', 'line 2: a URL not allowed'],
    ['%%{init: {"theme":"dark"}}%%\nflowchart LR\n  A --> B', 'line 1: a configuration directive (%%{…}%%) not allowed'],
    ['---\ntitle: ok\nconfig:\n  theme: dark\n---\nflowchart LR\n  A --> B', 'line 3: front matter other than a title ("config") not allowed'],
    // An indented --- does not close front matter for Mermaid, so the config after it is still front matter.
    ['---\ntitle: x\n  ---\nconfig:\n  securityLevel: loose\n---\nflowchart LR\n  A --> B', 'line 3: front matter other than a title ("---") not allowed'],
    ['flowchart LR\n  A --> B; click A callback', 'line 2: a click action not allowed'],
    ['flowchart LR\n  A --> B\n  classDef a{}*{fill:red} fill:red', 'line 3: this classDef not allowed'],
    ['mindmap\n  root\n    A::icon(fa fa-book)', 'line 3: an icon not allowed'],
  ])('refuses %j with a reason that names the line', (source, reason) => {
    expect(() => validateMermaid(source)).toThrow(reason)
  })
  it('ignores comment lines, and says why an empty or oversized diagram is refused', () => {
    expect(() => validateMermaid('flowchart LR\n  %% docs: https://example.invalid\n  A --> B')).not.toThrow()
    expect(() => validateMermaid('  \n')).toThrow('the diagram is empty')
    expect(() => validateMermaid('flowchart LR\n' + 'x'.repeat(20_000))).toThrow(/too large/)
  })
})

// Fix round 1 (review of card #84): the reviewer's payloads. Each one reached the network or a
// live element in the page before; each is refused now, with its line.
describe('Mermaid policy: review payloads', () => {
  it.each([
    // C1: shape blocks
    ['flowchart LR\n  A@{ "img": "//example.invalid/x.png", h: 60, w: 60 }', /line 2: /],
    ['flowchart LR\n  A@{ label: "}", img: "/x.png" }', /line 2: /],
    ['flowchart LR\n  A@{ "\\x69mg": "/x.png" }', /line 2: /],
    ['flowchart LR\n  A@{ icon: "fa:user", label: "x" }', 'line 2: the @{…} key "icon" not allowed'],
    ['flowchart LR\n  A@{ shape: rect, label: "a\\u0069" }', /line 2: this @\{…\} block/],
    ['flowchart LR\n  A@{ shape: rect', 'line 2: an unclosed @{…} block not allowed'],
    ['flowchart LR\n  A["see //example.invalid/x"]', 'line 2: a //host address not allowed'],
    // C2: a carriage return ends a comment for Mermaid
    ['flowchart LR\n  %% c\rclick A href "javascript:alert(1)"', 'line 3: a click action not allowed'],
    ['flowchart LR\n  %% c\rA["<img src=x onerror=alert(1)>"]', 'line 3: HTML not allowed'],
    ['flowchart LR\n  %% c\rA@{ img: "/x.png" }', 'line 3: the @{…} key "img" not allowed'],
    ['stateDiagram-v2\n  [*] --> A\n  %% c\rclassDef bad fill:red;mask-image:url(/x.png)', 'line 4: url() not allowed'],
    ['classDiagram\n  class A\n  %% c\u2028style A fill:red,mask-image:url(/x.png)', 'line 4: url() not allowed'],
    ['block-beta\n  a\n  %% c\u0085style a fill:red,mask-image:url(/x.png)', 'line 4: url() not allowed'],
    // I1: every declaration, split on , and ;
    ['stateDiagram-v2\n  [*] --> A\n  classDef bad fill:red;font-size:900px;position:fixed\n  class A bad', 'line 3: the style property "font-size" not allowed'],
    ['classDiagram\n  class A\n  style A fill:red;stroke-width:2px,position:fixed', 'line 3: the style property "position" not allowed'],
    ['block-beta\n  a\n  classDef c fill:#fff ; color:red ; mask:x\n  class a c', 'line 3: the style property "mask" not allowed'],
    // I2: Unicode spaces before keywords
    ['flowchart LR\n  A --> B\n\u00a0click A href "/x"', 'line 3: a click action not allowed'],
    ['flowchart LR\n  A --> B\n\u3000\u200bclick A call f()', 'line 3: a click action not allowed'],
    ['sequenceDiagram\n  A->>B: hi\n\u2003link A: x @ /y', 'line 3: a link not allowed'],
    ['flowchart LR\n  A --> B;\u00a0click A href "/x"', 'line 2: a click action not allowed'],
  ] as Array<[string, string | RegExp]>)('refuses %j', (source, reason) => {
    expect(() => validateMermaid(source)).toThrow(reason)
  })

  it('accepts shape blocks with literal values for the plain keys', () => {
    expect(validateMermaid('flowchart LR\n  A@{ shape: rounded, label: "Gateway: /api/orders" } --> B@{ shape: cyl, w: 60, h: 40 }\n  A e1@--> B\n  e1@{ animate: true }'))
      .toContain('shape: rounded')
  })

  it('validates and returns exactly what Mermaid will read: CRLF, quoted title, Unicode spaces', () => {
    const crlf = '---\r\ntitle: "Services"\r\n---\r\nflowchart LR\r\n  A[a\u00a0b] --> B\r\n'
    const drawn = validateMermaid(crlf)
    expect(drawn).toBe('---\ntitle: "Services"\n---\nflowchart LR\n  A[a b] --> B\n')
    expect(drawn).toBe(normalizeMermaid(crlf))
    expect(drawn).not.toMatch(/[\r\u00a0\u2028]/)
  })
})

// Corpus regressions: these are passive presentation values, not loading capabilities.
describe('Mermaid corpus compatibility', () => {
  it('allows only standard xml:space values among XML attributes', () => {
    for (const value of ['preserve', 'default']) {
      expect(sanitizeSvg(`<svg xmlns="http://www.w3.org/2000/svg"><text xml:space="${value}">A B</text></svg>`)).toContain(`xml:space="${value}"`)
    }
    for (const attribute of ['xml:space="unknown"', 'xml:base="/private"', 'xml:lang="en"']) {
      expect(() => sanitizeSvg(`<svg xmlns="http://www.w3.org/2000/svg"><text ${attribute}>A</text></svg>`)).toThrow('Unsupported SVG attribute')
    }
  })
  it('allows numeric quadrant radii and literal kanban assignment metadata', () => {
    expect(() => validateMermaid('quadrantChart\n classDef urgent color:#ff0000,radius:8\n A:::urgent: [0.2, 0.8]')).not.toThrow()
    expect(() => validateMermaid('kanban\n todo[To do]\n a[Draft]@{ assigned: "Writer", priority: "High" }')).not.toThrow()
    for (const source of ['quadrantChart\n classDef urgent radius:url(/private)',
      'kanban\n todo[To do]\n a[Draft]@{ assigned: "https://example.invalid" }',
      'kanban\n todo[To do]\n a[Draft]@{ icon: "fa:user" }']) {
      expect(() => validateMermaid(source)).toThrow()
    }
  })
})

describe('Mermaid statement separators', () => {
  // flow.jison: CLASS SPACE idString SPACE idString. Both operands accept comma,
  // Unicode, and punctuation tokens; neither is restricted to a CSS identifier.
  const classAssignments = [
    'é red', 'A red,blue', 'é,节点 red,blue', 'A 色',
    'A/B red', 'A red.blue', 'A:B red:blue', 'A&B red*', '123 456,789', 'A#1 default,v-1',
  ]
  it.each(classAssignments)('accepts the flowchart class operands %s after CSS', assignment => {
    const source = `flowchart LR\nA-->B\nclassDef red fill:red; class ${assignment}`
    expect(validateMermaid(source)).toBe(source)
  })
  it.each(classAssignments)('checks every style after class %s', assignment => {
    for (const header of ['flowchart LR', 'graph LR']) {
      for (const separator of ['; ', '\n']) {
        for (const statement of ['style B', 'classDef blue', 'linkStyle 0']) {
          const source = `${header}\nA-->B\nclassDef red fill:red; class ${assignment}${separator}${statement} fill:gold,position:fixed`
          expect(() => validateMermaid(source), source).toThrow('the style property "position" not allowed')
        }
        const source = `${header}\nA-->B\nclassDef red fill:red; class ${assignment}${separator}style B fill:var(--evil)`
        expect(() => validateMermaid(source), source).toThrow('the fill value "var(--evil)" not allowed')
      }
    }
  })
  it('checks every style on a line, separately from class assignments', () => {
    expect(() => validateMermaid('flowchart LR; A-->B; classDef blue fill:lightblue; class A blue; style B fill:gold; linkStyle 0 stroke:red')).not.toThrow()
    expect(() => validateMermaid('flowchart LR; A-->B; classDef blue fill:lightblue; class A blue; style B position:absolute')).toThrow('position')
    // Semicolons between declarations remain checked, as state styles require.
    expect(() => validateMermaid('stateDiagram-v2\n classDef blue fill:red;position:absolute')).toThrow('position')
  })
  it.each(['stateDiagram', 'stateDiagram-v2'])(
    'checks the entire CSS line in %s, including class-looking declarations', header => {
      for (const keyword of ['classDef blue', 'style A']) {
        for (const tail of [
          'class:foo;position:fixed',
          'class foo;fill:var(--evil)',
          'class foo;stroke:u\\72l(/private)',
          'class foo;fill:expression(1)',
          'style A fill:red;position:fixed',
          'classDef red fill:red;position:fixed',
        ]) {
          const source = `${header}\n[*] --> A\n${keyword} fill:red;${tail}\nclass A blue`
          expect(() => validateMermaid(source), source).toThrow(/line 3: .*not allowed/)
        }
      }
    },
  )
  it('checks styles after real class assignments on the same line and later lines', () => {
    expect(() => validateMermaid('flowchart LR; A-->B; class A blue; style A fill:red; class B blue; style B position:fixed')).toThrow('position')
    expect(() => validateMermaid('stateDiagram-v2\n[*] --> A\nclass A blue\nclassDef blue fill:red;stroke:var(--evil)')).toThrow('stroke value')
    expect(() => validateMermaid('stateDiagram-v2\n[*] --> A\nclassDef blue fill:red;stroke:blue;stroke-width:2px\nclass A blue')).not.toThrow()
    expect(() => validateMermaid('---\ntitle: State colors\n---\n%% comment\nSTATEdiagram-v2\nclassDef blue fill:red;class:foo;position:fixed')).toThrow('line 6:')
  })
  it('accepts newline-separated shape literals and refuses loading keys there', () => {
    expect(() => validateMermaid('flowchart LR\n A@{\n shape: hex\n label: "Review image"\n }')).not.toThrow()
    expect(() => validateMermaid('flowchart LR\n A@{\n shape: hex\n img: "/private"\n }')).toThrow('"img"')
  })
})

// BLG-329: what the dataviz skill writes must draw; the refusal names what to fix.
describe('Vega-Lite charts the viewer can draw', () => {
  const values = [
    {month: 'Jan', team: 'A', open: 3, closed: 5}, {month: 'Feb', team: 'A', open: 4, closed: 2},
    {month: 'Jan', team: 'B', open: 1, closed: 6}, {month: 'Feb', team: 'B', open: 7, closed: 3},
  ]
  const bar = {mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}
  const accepted: Array<[string, Record<string, unknown>]> = [
    ['a themed bar chart', {
      $schema: 'https://vega.github.io/schema/vega-lite/v6.json', data: {values}, ...bar,
      config: {
        background: '#ffffff', font: 'Inter, "Helvetica Neue", sans-serif',
        range: {category: ['#2f6fdb', '#d9622b', '#3a9d5d'], ramp: {scheme: 'blues', count: 5}},
        axis: {labelColor: '#43566d', titleColor: '#172130', gridColor: '#dbe1e9', labelFont: 'Inter', titleFontSize: 12, domain: false, gridDash: [2, 2]},
        axisX: {labelAngle: 0}, legend: {orient: 'bottom', labelFont: 'Inter', symbolType: 'circle'},
        title: {anchor: 'start', fontSize: 14, subtitleColor: '#43566d'}, view: {stroke: null, continuousWidth: 400},
        bar: {color: '#2f6fdb', cornerRadiusEnd: 2}, line: {strokeWidth: 2, point: {filled: true}},
        style: {'guide-label': {fill: '#43566d'}}, padding: 8, autosize: {type: 'fit', contains: 'padding'},
      },
    }],
    ['a layered line with a rule annotation', {
      data: {values},
      layer: [
        {mark: {type: 'line', point: true}, encoding: {x: {field: 'month', type: 'ordinal'}, y: {field: 'open', type: 'quantitative', aggregate: 'sum'}}},
        {mark: {type: 'rule', strokeDash: [4, 4], color: '#d9622b'}, encoding: {y: {datum: 5}}},
        {mark: {type: 'text', align: 'left', dx: 4}, encoding: {y: {datum: 5}, text: {value: 'target'}}},
      ],
    }],
    ['faceted small multiples', {data: {values}, facet: {field: 'team', type: 'nominal'}, columns: 2, spec: {width: 160, height: 120, ...bar}}],
    ['a row/column facet', {data: {values}, facet: {row: {field: 'team', type: 'nominal'}}, spec: bar}],
    ['an hconcat of two charts', {data: {values}, hconcat: [bar, {mark: 'point', encoding: {x: {field: 'open', type: 'quantitative'}, y: {field: 'closed', type: 'quantitative'}}}]}],
    ['a repeat over fields', {data: {values}, repeat: ['open', 'closed'], spec: {mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: {repeat: 'repeat'}, type: 'quantitative'}}}}],
    ['a conditional color with an object test', {data: {values}, ...bar, encoding: {...bar.encoding, color: {condition: {test: {field: 'open', gt: 3}, value: '#d9622b'}, value: '#2f6fdb'}}}],
    ['a stacked area of 50 keys by 5 groups', {
      data: {values: Array.from({length: 250}, (_, i) => ({day: i % 50, team: `t${Math.floor(i / 50)}`, n: i % 7}))},
      mark: 'area', encoding: {x: {field: 'day', type: 'quantitative'}, y: {field: 'n', type: 'quantitative', aggregate: 'sum'}, color: {field: 'team', type: 'nominal'}},
    }],
    ['a stacked area whose key is proven: 10 ordinal keys, 3,000 measure values, 50 groups', {
      // Taking the larger of x and y (3,000 x 50) would refuse it; y is provably the stacked channel.
      data: {values: Array.from({length: 3000}, (_, i) => ({k: i % 10, v: i, t: i % 50}))},
      mark: 'area', encoding: {x: {field: 'k', type: 'ordinal'}, y: {field: 'v', type: 'quantitative'}, color: {field: 't', type: 'nominal'}},
    }],
    ['a stacked area whose key is proven by the only aggregate', {
      data: {values: Array.from({length: 3000}, (_, i) => ({k: i % 10, v: i, t: i % 50}))},
      mark: 'area', encoding: {x: {field: 'k', type: 'quantitative'}, y: {field: 'v', type: 'quantitative', aggregate: 'sum'}, color: {field: 't', type: 'nominal'}},
    }],
    ['a facet of 6', {data: {values: Array.from({length: 12}, (_, i) => ({k: `k${i % 6}`, open: i, month: 'Jan'}))}, facet: {field: 'k', type: 'nominal'}, columns: 3, spec: bar}],
    ['a repeat of 4 inside an hconcat of 2 (8 panels)', {data: {values}, hconcat: [
      {repeat: {row: ['open', 'closed'], column: ['open', 'closed']}, spec: {mark: 'point', encoding: {x: {field: {repeat: 'column'}, type: 'quantitative'}, y: {field: {repeat: 'row'}, type: 'quantitative'}}}},
      bar,
    ]}],
    ['datasets referenced by name', {datasets: {tickets: values}, data: {name: 'tickets'}, ...bar}],
    ['checked dates in a datum, a scale domain, axis values, a sort and a timed test', {
      data: {values: [{when: '2026-01-05', n: 1}, {when: '2026-03-02', n: 4}]},
      layer: [
        {mark: 'line', encoding: {color: {condition: {test: {field: 'when', timeUnit: 'month', range: [{month: 'jan'}, {month: 3}]}, value: '#d9622b'}, value: '#2f6fdb'},
          x: {field: 'when', type: 'temporal', timeUnit: 'yearmonth', sort: [{year: 2026, month: 1}],
            axis: {values: [{year: 2026, month: 1}, {year: 2026, month: 3}]},
          scale: {domain: [{year: 2026, month: 1}, {year: 2026, month: 'December', date: 31, utc: true}]}}, y: {field: 'n', type: 'quantitative'}}},
        {mark: 'rule', encoding: {x: {datum: {year: 2026, month: 2, date: 1, hours: 12, minutes: 30, seconds: 0, milliseconds: 0}, type: 'temporal'}}},
      ],
    }],
  ]
  it.each(accepted)('accepts %s', (_name, spec) => {
    expect(() => parseVegaLite(JSON.stringify(spec))).not.toThrow()
  })

  it.each(accepted)('lays out %s with the real renderer, without loading anything', async (_name, spec) => {
    const [{compile}, {View, parse, logger}, {expressionInterpreter}, {denyVisualResources}] = await Promise.all([
      import('vega-lite'), import('vega'), import('vega-interpreter'), import('./vega')])
    const compiled = compile(parseVegaLite(JSON.stringify(spec)) as never, {logger: logger(0)}).spec
    const view = new View(parse(compiled, undefined, {ast: true}), {renderer: 'none', loader: denyVisualResources, logger: logger(0), expr: expressionInterpreter})
    try {
      await expect(view.runAsync()).resolves.toBe(view)
    } finally { view.finalize() }
  })

  it('fits single and layered charts to the message and lets compositions keep their panel sizes', () => {
    const layered = parseVegaLite(JSON.stringify({data: {values}, layer: [bar]}))
    expect(layered).toMatchObject({width: 640, height: 360, autosize: {type: 'fit'}, padding: 16, background: 'transparent'})
    const concat = parseVegaLite(JSON.stringify({data: {values}, hconcat: [bar, bar]}))
    expect(concat).not.toHaveProperty('width')
    expect(concat).toMatchObject({autosize: {type: 'pad'}, padding: 16, background: 'transparent'})
  })
})
