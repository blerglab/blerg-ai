import { describe, it, expect } from 'vitest'
import { playToastSound, playMessageSound } from './sound'

describe('playToastSound', () => {
  it('is a safe no-op where Web Audio is unavailable', () => {
    // jsdom has no AudioContext; playing must never throw.
    expect(() => playToastSound()).not.toThrow()
  })
})

describe('playMessageSound', () => {
  it('is a safe no-op where Web Audio is unavailable', () => {
    expect(() => playMessageSound()).not.toThrow()
  })
})
