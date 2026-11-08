import { describe, expect, it } from 'vitest'
import { parseSequence } from './parse'

describe('parseSequence — supported subset', () => {
  it('parses explicit participants and a plain message', () => {
    const graph = parseSequence('sequenceDiagram\nparticipant Alice\nparticipant Bob\nAlice->>Bob: Hello')
    expect(graph?.participants).toEqual([{ id: 'Alice', label: 'Alice', actor: false }, { id: 'Bob', label: 'Bob', actor: false }])
    expect(graph?.events).toEqual([{ kind: 'message', from: 'Alice', to: 'Bob', label: 'Hello', style: 'solid', activate: false, deactivate: false }])
  })

  it('implicitly declares a participant on first mention, in first-appearance order', () => {
    const graph = parseSequence('sequenceDiagram\nAlice->>Bob: Hi\nBob-->>Carol: Forward')
    expect(graph?.participants.map(p => p.id)).toEqual(['Alice', 'Bob', 'Carol'])
  })

  it('an actor keyword marks that participant drawn as an actor', () => {
    const graph = parseSequence('sequenceDiagram\nactor Reader\nparticipant API\nReader->>API: Request')
    expect(graph?.participants).toEqual([{ id: 'Reader', label: 'Reader', actor: true }, { id: 'API', label: 'API', actor: false }])
  })

  it('an alias from "as" becomes the label, not the id', () => {
    const graph = parseSequence('sequenceDiagram\nparticipant API as Report API\nAPI->>API: Self')
    expect(graph?.participants[0]).toEqual({ id: 'API', label: 'Report API', actor: false })
  })

  it('distinguishes sync (solid) and async (dashed) arrows', () => {
    const graph = parseSequence('sequenceDiagram\nA->>B: sync\nB-->>A: async')
    expect(graph?.events.map(e => (e as { style?: string }).style)).toEqual(['solid', 'dashed'])
  })

  it('a message with no text has an empty label, not a parse failure', () => {
    const graph = parseSequence('sequenceDiagram\nA->>B:')
    expect((graph?.events[0] as { label: string | null }).label).toBe('')
  })

  it('the "+" activation shorthand activates the target, right after the message', () => {
    const graph = parseSequence('sequenceDiagram\nAlice->>+Bob: Hello')
    expect(graph?.events).toEqual([
      { kind: 'message', from: 'Alice', to: 'Bob', label: 'Hello', style: 'solid', activate: true, deactivate: false },
      { kind: 'activate', id: 'Bob' },
    ])
  })

  it('the "-" activation shorthand deactivates the source, right after the message', () => {
    const graph = parseSequence('sequenceDiagram\nBob-->>-Alice: Great!')
    expect(graph?.events).toEqual([
      { kind: 'message', from: 'Bob', to: 'Alice', label: 'Great!', style: 'dashed', activate: false, deactivate: true },
      { kind: 'deactivate', id: 'Bob' },
    ])
  })

  it('explicit activate/deactivate statements parse the same as the shorthand', () => {
    const graph = parseSequence('sequenceDiagram\nactivate Bob\nAlice->>Bob: Hi\ndeactivate Bob')
    expect(graph?.events.map(e => e.kind)).toEqual(['activate', 'message', 'deactivate'])
  })

  it('a note over one participant', () => {
    const graph = parseSequence('sequenceDiagram\nparticipant Store\nNote over Store: Data ready')
    expect(graph?.events[0]).toEqual({ kind: 'note', position: 'over', participants: ['Store'], label: 'Data ready' })
  })

  it('a note over two participants', () => {
    const graph = parseSequence('sequenceDiagram\nA->>B: hi\nNote over A,B: Shared note')
    expect(graph?.events.at(-1)).toEqual({ kind: 'note', position: 'over', participants: ['A', 'B'], label: 'Shared note' })
  })

  it('a note left of / right of one participant', () => {
    const graph = parseSequence('sequenceDiagram\nA->>B: hi\nNote right of B: Rational thoughts!')
    expect(graph?.events.at(-1)).toEqual({ kind: 'note', position: 'right', participants: ['B'], label: 'Rational thoughts!' })
  })

  it('one level of loop, containing a self-message', () => {
    const graph = parseSequence('sequenceDiagram\nAlice->>John: Hi\nloop HealthCheck\n  John->>John: Fight hypochondria\nend\nJohn-->>Alice: Great!')
    const loop = graph?.events[1]
    expect(loop?.kind).toBe('block')
    if (loop?.kind === 'block') {
      expect(loop.blockKind).toBe('loop')
      expect(loop.label).toBe('HealthCheck')
      expect(loop.items).toEqual([{ kind: 'message', from: 'John', to: 'John', label: 'Fight hypochondria', style: 'solid', activate: false, deactivate: false }])
    }
  })

  it('one level of opt', () => {
    const graph = parseSequence('sequenceDiagram\nopt Maybe\n  A->>B: Maybe msg\nend')
    expect(graph?.events[0]).toMatchObject({ kind: 'block', blockKind: 'opt', label: 'Maybe' })
  })

  it('one level of alt with an else branch', () => {
    const graph = parseSequence('sequenceDiagram\nalt success\n  A->>B: OK\nelse failure\n  A->>B: Fail\nend')
    const block = graph?.events[0]
    expect(block).toMatchObject({ kind: 'block', blockKind: 'alt', label: 'success', elseLabel: 'failure' })
    if (block?.kind === 'block') {
      expect(block.items).toEqual([{ kind: 'message', from: 'A', to: 'B', label: 'OK', style: 'solid', activate: false, deactivate: false }])
      expect(block.elseItems).toEqual([{ kind: 'message', from: 'A', to: 'B', label: 'Fail', style: 'solid', activate: false, deactivate: false }])
    }
  })

  it('an alt with no else has no elseItems', () => {
    const graph = parseSequence('sequenceDiagram\nalt only\n  A->>B: OK\nend')
    const block = graph?.events[0]
    expect(block?.kind).toBe('block')
    expect((block as { elseLabel?: string }).elseLabel).toBeUndefined()
    expect((block as { elseItems?: unknown[] }).elseItems).toBeUndefined()
  })

  it('ignores full-line comments and frontmatter', () => {
    const graph = parseSequence(['---', 'title: "Generic"', '---', '%% a comment', 'sequenceDiagram', 'A->>B: hi'].join('\n'))
    expect(graph?.events).toHaveLength(1)
  })

  it('keeps unicode ids, aliases and labels as-is', () => {
    const graph = parseSequence('sequenceDiagram\nparticipant Reader as Lecteur 🚀\nReader->>Reader: Café résumé Ω')
    expect(graph?.participants[0].label).toBe('Lecteur 🚀')
    expect((graph?.events[0] as { label: string | null }).label).toBe('Café résumé Ω')
  })

  it('decodes entities and <br> in a message label', () => {
    const graph = parseSequence('sequenceDiagram\nA->>B: First<br/>Second &amp; more')
    expect((graph?.events[0] as { label: string | null }).label).toBe('First\nSecond & more')
  })
})

describe('parseSequence — falls back (null), never a partial draw', () => {
  const cases: Record<string, string> = {
    'not a sequence diagram at all': 'flowchart LR\nA --> B',
    'par/and is unsupported': 'sequenceDiagram\npar First\nA->>B: hi\nand Second\nB->>A: hi\nend',
    'nesting a block inside a block is unsupported': 'sequenceDiagram\nalt one\nloop two\nA->>B: hi\nend\nend',
    'box grouping is unsupported': 'sequenceDiagram\nbox Clients\nparticipant A\nend\nA->>A: hi',
    'autonumber is unsupported': 'sequenceDiagram\nautonumber\nA->>B: hi',
    'a lost message (-x) is unsupported': 'sequenceDiagram\nA-xB: gone',
    'a lost message (--x) is unsupported': 'sequenceDiagram\nA--xB: gone',
    'a fire-and-forget message (-)) is unsupported': 'sequenceDiagram\nA-)B: gone',
    'a second else is unsupported': 'sequenceDiagram\nalt one\nA->>B: hi\nelse two\nA->>B: hi\nelse three\nA->>B: hi\nend',
    'an else on opt is unsupported': 'sequenceDiagram\nopt one\nA->>B: hi\nelse two\nA->>B: hi\nend',
    'an end with no open block': 'sequenceDiagram\nA->>B: hi\nend',
    'a block never closed': 'sequenceDiagram\nalt one\nA->>B: hi',
    'an unrecognized line': 'sequenceDiagram\nthis is not a known production',
  }
  for (const [name, source] of Object.entries(cases)) {
    it(name, () => { expect(parseSequence(source)).toBeNull() })
  }

  it('the whole diagram still falls back even when most lines are recognized', () => {
    const graph = parseSequence('sequenceDiagram\nparticipant A\nparticipant B\nA->>B: ok\npar should fail\nA->>B: hi\nend')
    expect(graph).toBeNull()
  })
})
