import { describe, expect, it } from 'vitest'
import { cardNumbers } from './cardRefs'

describe('cardNumbers (the chat\'s tokenizer, #153; shared with the checklist and its auto-tick)', () => {
  it('reads each "#N" a text names, once, never inside a word or an entity', () => {
    expect(cardNumbers('#155 Dismiss in Needs you')).toEqual([155])
    expect(cardNumbers('Close #12 and #7, then #12 again')).toEqual([12, 7])
    expect(cardNumbers('a#1 &#147; #1a #0 step')).toEqual([])
    expect(cardNumbers('Merge 08025f2c')).toEqual([])
  })
})
