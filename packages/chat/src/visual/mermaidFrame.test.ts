// parseMeasured (code review L5): the frame is the untrusted side -- it ran Mermaid against
// attacker-influenced diagram source, so a degenerate measurement (NaN, Infinity, zero, negative)
// must never reach boundedSize()'s division/Math.max on the main thread unexamined.
import { describe, expect, it } from 'vitest'
import { parseMeasured } from './mermaidFrame'

describe('parseMeasured', () => {
  it('accepts a finite, positive width and height', () => {
    expect(parseMeasured({ width: 640, height: 240 })).toEqual({ width: 640, height: 240 })
  })
  it('rejects null, missing fields and non-object values', () => {
    expect(parseMeasured(null)).toBeNull()
    expect(parseMeasured(undefined)).toBeNull()
    expect(parseMeasured({})).toBeNull()
    expect(parseMeasured({ width: 640 })).toBeNull()
    expect(parseMeasured('640x240')).toBeNull()
  })
  it.each([
    ['NaN width', { width: NaN, height: 240 }],
    ['Infinity width', { width: Infinity, height: 240 }],
    ['-Infinity height', { width: 640, height: -Infinity }],
    ['zero width', { width: 0, height: 240 }],
    ['negative height', { width: 640, height: -10 }],
    ['non-numeric width', { width: '640', height: 240 }],
  ])('rejects %s', (_name, measured) => {
    expect(parseMeasured(measured)).toBeNull()
  })
})
