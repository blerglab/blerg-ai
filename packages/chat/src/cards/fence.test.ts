import { describe, expect, it } from 'vitest'
import { cardKind, parseCardFence } from './fence'

describe('cardKind', () => {
  it('reads the kind off the class react-markdown gives a card fence', () => {
    expect(cardKind('language-card:character')).toBe('character')
    expect(cardKind('language-card:scene-beat')).toBe('scene-beat')
  })
  it('is null for anything that is not a card fence', () => {
    expect(cardKind(undefined)).toBeNull()
    expect(cardKind('')).toBeNull()
    expect(cardKind('language-ts')).toBeNull()
    expect(cardKind('language-card')).toBeNull()
    expect(cardKind('language-card:')).toBeNull()
    expect(cardKind('card:character')).toBeNull()
  })
})

describe('parseCardFence', () => {
  it('returns the kind and the parsed object', () => {
    expect(parseCardFence('language-card:character', '{"name": "Mara Vance", "role": "antagonist"}\n'))
      .toEqual({ kind: 'character', data: { name: 'Mara Vance', role: 'antagonist' } })
  })
  it('keeps an unknown kind: the registry decides what to do with it', () => {
    expect(parseCardFence('language-card:whatever', '{}')).toEqual({ kind: 'whatever', data: {} })
  })
  it('is null for a fence that is not a card', () => {
    expect(parseCardFence(undefined, '{"a":1}')).toBeNull()
    expect(parseCardFence('language-json', '{"a":1}')).toBeNull()
  })
  it('is null for a body that is not strict JSON', () => {
    expect(parseCardFence('language-card:x', '{name: "Mara"}')).toBeNull()
    expect(parseCardFence('language-card:x', "{'name': 'Mara'}")).toBeNull()
    expect(parseCardFence('language-card:x', '{"name": "Ma')).toBeNull()
    expect(parseCardFence('language-card:x', '')).toBeNull()
    expect(parseCardFence('language-card:x', '{"a":1} trailing')).toBeNull()
  })
  it('is null for JSON that is not a plain object', () => {
    expect(parseCardFence('language-card:x', '[1, 2]')).toBeNull()
    expect(parseCardFence('language-card:x', '"text"')).toBeNull()
    expect(parseCardFence('language-card:x', '42')).toBeNull()
    expect(parseCardFence('language-card:x', 'null')).toBeNull()
    expect(parseCardFence('language-card:x', 'true')).toBeNull()
  })
})
