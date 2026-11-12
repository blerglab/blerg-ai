import { describe, expect, it } from 'vitest'
import { labelRefKey } from './refChip'

describe('labelRefKey', () => {
  it('matches a full key', () => { expect(labelRefKey('BLG-468')).toBe('card:BLG:468') })
  it('matches a bare #N', () => { expect(labelRefKey('#147')).toBe('card:147') })
  it('matches an explicit board:ID#N', () => { expect(labelRefKey('board:abc-123#9')).toBe('card:abc-123:9') })
  it('matches a commit SHA', () => { expect(labelRefKey('08025f2c')).toBe('commit:08025f2c') })
  it('trims surrounding whitespace', () => { expect(labelRefKey('  BLG-468  ')).toBe('card:BLG:468') })
  it('does not match a plain label', () => { expect(labelRefKey('Start')).toBeNull() })
  it('does not match a label that merely contains a key', () => { expect(labelRefKey('see BLG-468 for details')).toBeNull() })
  it('does not match an empty label', () => { expect(labelRefKey('')).toBeNull() })
})
