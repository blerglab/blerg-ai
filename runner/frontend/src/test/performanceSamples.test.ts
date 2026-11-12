import { expect, it } from 'vitest'
import { medianSample } from './performanceSamples'

it('does not charge an isolated scheduler interruption to typing', () => {
 expect(medianSample([2, 3, 36.5, 4, 5])).toBe(4)
})
it('keeps consistently slow typing over the unchanged 16 ms budget', () => {
 expect(medianSample([20, 21, 22, 23, 24])).toBeGreaterThanOrEqual(16)
})
it('uses both central samples for an even sample count', () => {
 expect(medianSample([4, 1, 3, 2])).toBe(2.5)
})
