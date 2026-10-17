import { describe, expect, it } from 'vitest'
import { countLines, firstLines, messageNeedsCap } from './lineCap'

const lines = (n: number) => Array.from({ length: n }, (_, i) => `line ${i + 1}`).join('\n')

describe('lineCap', () => {
  it('counts lines without the trailing newline', () => {
    expect(countLines('')).toBe(0)
    expect(countLines('a')).toBe(1)
    expect(countLines('a\nb\n')).toBe(2)
  })
  it('takes the first lines', () => {
    expect(firstLines(lines(30), 3)).toBe('line 1\nline 2\nline 3')
  })
  it('caps a message over 25 lines, not one of 25', () => {
    expect(messageNeedsCap(lines(26))).toBe(true)
    expect(messageNeedsCap(lines(25))).toBe(false)
  })
  it('leaves a long closed fence to the block itself', () => {
    expect(messageNeedsCap('look:\n```go\n' + lines(40) + '\n```\nthanks')).toBe(false)
    expect(messageNeedsCap(lines(30) + '\n```go\n' + lines(40) + '\n```')).toBe(true)
  })
})
