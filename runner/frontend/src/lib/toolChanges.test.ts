import { describe, it, expect } from 'vitest'
import { escapeHidden, isPlainName, countPendingToolChanges, wordDiff, lineDiff, schemaText, unsafeReason, acceptFailureMessage } from './toolChanges'

describe('escapeHidden', () => {
  it('leaves plain text alone', () => {
    expect(escapeHidden('Reads a file\nand a second line\twith a tab')).toEqual({ text: 'Reads a file\nand a second line\twith a tab', hidden: false })
  })
  // Review Focus 1
  it('shows bidi, tag, zero-width and private-use code points as escapes', () => {
    const r = escapeHidden('a\u202Eb \u200Bc \u{E0041}d \uE000e')
    expect(r.hidden).toBe(true)
    expect(r.text).toBe('a⟨U+202E⟩b ⟨U+200B⟩c ⟨U+E0041⟩d ⟨U+E000⟩e')
  })
  it('shows other control characters and the line separators', () => {
    expect(escapeHidden('x\u0007y\u2028z').text).toBe('x⟨U+0007⟩y⟨U+2028⟩z')
  })
  it('keeps ordinary non-ASCII letters', () => {
    expect(escapeHidden('café — naïve 日本')).toEqual({ text: 'café — naïve 日本', hidden: false })
  })
})

describe('isPlainName', () => {
  it('accepts ASCII printable names and refuses the rest', () => {
    expect(isPlainName('read_file-v2')).toBe(true)
    expect(isPlainName('read\u200Bfile')).toBe(false)
    expect(isPlainName('lire')).toBe(true)
    expect(isPlainName('lïre')).toBe(false)
  })
})

describe('diffs', () => {
  it('wordDiff marks added and removed words', () => {
    const parts = wordDiff('reads a file', 'reads a file and deletes it')
    expect(parts.filter(p => p.kind === 'add').map(p => p.text).join('')).toBe(' and deletes it')
    expect(parts.some(p => p.kind === 'del')).toBe(false)
    const d2 = wordDiff('send mail', 'send invoice')
    expect(d2.find(p => p.kind === 'del')?.text).toBe('mail')
    expect(d2.find(p => p.kind === 'add')?.text).toBe('invoice')
  })
  it('lineDiff marks changed lines', () => {
    const parts = lineDiff('{\n  "a": 1\n}', '{\n  "a": 2\n}')
    expect(parts.filter(p => p.kind === 'del').map(p => p.text)).toEqual(['  "a": 1'])
    expect(parts.filter(p => p.kind === 'add').map(p => p.text)).toEqual(['  "a": 2'])
  })
})

describe('schemaText and unsafeReason', () => {
  it('pretty prints the schema from the canonical text and survives nulls', () => {
    const def = { hash: 'h', name: 't', description: 'd', input_schema: { type: 'object' }, canonical: '{"description":"d","inputSchema":{"type":"object","properties":{"n":{"type":"number","default":1E+2}}},"name":"t"}' }
    expect(schemaText(def)).toContain('"default": 1E+2')
    expect(schemaText(null)).toBe('null')
    expect(schemaText({ ...def, canonical: '', input_schema: null })).toBe('null')
  })
  it('explains why a definition cannot be reviewed', () => {
    expect(unsafeReason('hidden_chars')).toMatch(/hidden characters/)
    expect(unsafeReason('too_large')).toMatch(/too large/)
    expect(unsafeReason('nul')).toMatch(/cannot be stored/)
    expect(unsafeReason(undefined)).toBeNull()
    expect(unsafeReason('')).toBeNull()
  })
})

describe('acceptFailureMessage', () => {
  it('maps the server statuses', () => {
    expect(acceptFailureMessage(409, 'the tool changed again since this change was recorded')).toMatch(/changed again/)
    expect(acceptFailureMessage(410, 'the session has ended')).toMatch(/session has ended/)
    expect(acceptFailureMessage(400, 'never widen')).toMatch(/never widen/)
    expect(acceptFailureMessage(401, 'x')).toMatch(/Sign in again/)
  })
})

describe('countPendingToolChanges', () => {
  it('counts unmuted rows that are not removed', () => {
    expect(countPendingToolChanges([{ kind: 'changed' }, { kind: 'new', muted: true }, { kind: 'removed' }, { kind: 'changed', muted: false }])).toBe(2)
    expect(countPendingToolChanges(undefined)).toBe(0)
  })
})
