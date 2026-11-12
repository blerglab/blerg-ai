import { describe, expect, it } from 'vitest'
import { parseFlowchart } from './parse'

describe('parseFlowchart — supported subset', () => {
  it('parses every declared direction', () => {
    for (const [token, expected] of [['TD', 'TB'], ['TB', 'TB'], ['LR', 'LR'], ['RL', 'RL'], ['BT', 'BT']] as const) {
      const graph = parseFlowchart(`flowchart ${token}\nA --> B`)
      expect(graph?.direction, token).toBe(expected)
    }
  })

  it('parses a plain edge with no label', () => {
    const graph = parseFlowchart('flowchart TD\nA[Start] --> B[Finish]')
    expect(graph?.nodes).toEqual([
      { id: 'A', label: 'Start', shape: 'rect' },
      { id: 'B', label: 'Finish', shape: 'rect' },
    ])
    expect(graph?.edges).toEqual([{ from: 'A', to: 'B', label: null, style: 'solid' }])
  })

  it('parses all six declared node shapes', () => {
    const graph = parseFlowchart([
      'flowchart LR',
      'A[Rect] --> B(Round)',
      'B --> C([Stadium])',
      'C --> D((Circle))',
      'D --> E{Diamond}',
      'E --> F[[Subroutine]]',
    ].join('\n'))
    const shapes = Object.fromEntries((graph?.nodes ?? []).map(n => [n.id, n.shape]))
    expect(shapes).toEqual({ A: 'rect', B: 'round', C: 'stadium', D: 'circle', E: 'diamond', F: 'subroutine' })
  })

  it('parses the pipe edge-label form', () => {
    const graph = parseFlowchart('flowchart LR\nA[Hard] -->|Text| B(Round)')
    expect(graph?.edges).toEqual([{ from: 'A', to: 'B', label: 'Text', style: 'solid' }])
  })

  it('parses the open inline edge-label form', () => {
    const graph = parseFlowchart('flowchart LR\nA -- some text --> B')
    expect(graph?.edges).toEqual([{ from: 'A', to: 'B', label: 'some text', style: 'solid' }])
  })

  it('parses all four plain arrow styles', () => {
    const graph = parseFlowchart(['flowchart LR', 'A --> B', 'B --- C', 'C -.-> D', 'D ==> E'].join('\n'))
    expect(graph?.edges.map(e => e.style)).toEqual(['solid', 'line', 'dotted', 'thick'])
  })

  it('parses chained edges as sugar for consecutive pairs', () => {
    const graph = parseFlowchart('flowchart LR\nA --> B --> C')
    expect(graph?.edges).toEqual([
      { from: 'A', to: 'B', label: null, style: 'solid' },
      { from: 'B', to: 'C', label: null, style: 'solid' },
    ])
  })

  it('parses one level of subgraph membership', () => {
    const graph = parseFlowchart([
      'flowchart TB',
      'subgraph outer[Platform]',
      'A[Queue] --> B[Worker]',
      'end',
      'C[Store]',
      'B --> C',
    ].join('\n'))
    expect(graph?.subgraphs).toEqual([{ id: 'outer', label: 'Platform' }])
    expect(graph?.nodes.find(n => n.id === 'A')?.subgraph).toBe('outer')
    expect(graph?.nodes.find(n => n.id === 'B')?.subgraph).toBe('outer')
    expect(graph?.nodes.find(n => n.id === 'C')?.subgraph).toBeUndefined()
  })

  it('a bare node declaration just declares its shape', () => {
    const graph = parseFlowchart('flowchart LR\nA[Start]\nA --> B')
    expect(graph?.nodes.find(n => n.id === 'A')).toEqual({ id: 'A', label: 'Start', shape: 'rect' })
  })

  it('ignores full-line comments and frontmatter', () => {
    const graph = parseFlowchart(['---', 'title: "Generic diagram"', '---', '%% a comment', 'flowchart LR', 'A[Hard] -->|Text| B(Round)'].join('\n'))
    expect(graph?.nodes.map(n => n.id)).toEqual(['A', 'B'])
  })

  it('decodes entities and <br> in labels', () => {
    const graph = parseFlowchart('flowchart LR\nA["First<br/>Second"] --> B["Amp &amp; quote"]')
    expect(graph?.nodes.map(n => n.label)).toEqual(['First\nSecond', 'Amp & quote'])
  })

  it('keeps unicode labels and ids as-is', () => {
    const graph = parseFlowchart('flowchart LR\nA["Résumé café Ω 東京 🚀"] --> B["مرحبا שלום"]')
    expect(graph?.nodes.map(n => n.label)).toEqual(['Résumé café Ω 東京 🚀', 'مرحبا שלום'])
  })

  it('a quoted label may contain the shape\'s own delimiter characters', () => {
    const graph = parseFlowchart('flowchart LR\nA["brackets [two] inside"] --> B')
    expect(graph?.nodes.find(n => n.id === 'A')?.label).toBe('brackets [two] inside')
  })
})

describe('parseFlowchart — falls back (null), never a partial draw', () => {
  const cases: Record<string, string> = {
    'not a flowchart at all': 'sequenceDiagram\nAlice->>Bob: Hi',
    'nested subgraphs': 'flowchart TB\nsubgraph outer\ndirection LR\nsubgraph inner\nA --> B\nend\nC\nend\nB --> C',
    'a node literally named end': 'flowchart LR\nend --> A',
    'node-list fan-out': 'flowchart LR\nA & B --> C',
    'bidirectional arrow': 'flowchart LR\nA <--> B',
    classDef: 'flowchart LR\nA --> B\nclassDef blue fill:lightblue\nclass A blue',
    style: 'flowchart LR\nA --> B\nstyle B fill:gold',
    linkStyle: 'flowchart LR\nA --> B\nlinkStyle 0 stroke:red',
    'class statement': 'flowchart LR\nA --> B\nclass A blue',
    'inline class shorthand (:::)': 'flowchart LR\nA[image]:::blue --> B[Done]',
    'direction statement inside a subgraph': 'flowchart TB\nsubgraph outer\ndirection LR\nA --> B\nend',
    '@{...} shape metadata': 'flowchart LR\nA@{ shape: hex, label: "x" } --> B',
    'cylinder shape (not in the declared six)': 'flowchart LR\nA[(Store)] --> B',
    'asymmetric shape (not in the declared six)': 'flowchart LR\nA --> B>Result]',
    'markdown-string label': 'flowchart LR\nA["`**Bold**`"] --> B',
    '<br> is fine but other HTML is not': 'flowchart LR\nA["<b>bold</b>"] --> B',
    'legacy ampersand-less entity shorthand': 'flowchart LR\nA["#quot;quoted#quot;"] --> B',
    'an unterminated subgraph': 'flowchart TB\nsubgraph outer\nA --> B',
    'empty source': 'flowchart LR\n',
  }
  for (const [name, source] of Object.entries(cases)) {
    it(name, () => { expect(parseFlowchart(source)).toBeNull() })
  }
})
