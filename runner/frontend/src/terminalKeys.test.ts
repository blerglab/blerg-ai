import { describe, it, expect } from 'vitest'
import { arrowSeq, KEY_SEQ } from './terminalKeys'

describe('arrowSeq', () => {
  it('uses CSI (\\x1b[) prefix in normal cursor mode', () => {
    expect(arrowSeq('up', false)).toBe('\x1b[A')
    expect(arrowSeq('down', false)).toBe('\x1b[B')
    expect(arrowSeq('right', false)).toBe('\x1b[C')
    expect(arrowSeq('left', false)).toBe('\x1b[D')
  })
  it('uses SS3 (\\x1bO) prefix in application cursor mode', () => {
    expect(arrowSeq('up', true)).toBe('\x1bOA')
    expect(arrowSeq('down', true)).toBe('\x1bOB')
    expect(arrowSeq('right', true)).toBe('\x1bOC')
    expect(arrowSeq('left', true)).toBe('\x1bOD')
  })
})

describe('KEY_SEQ', () => {
  it('maps control keys to their bytes', () => {
    expect(KEY_SEQ.enter).toBe('\r')
    expect(KEY_SEQ.esc).toBe('\x1b')
    expect(KEY_SEQ.tab).toBe('\t')
  })
})
