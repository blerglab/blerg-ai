import { expect, it } from 'vitest'
import { blackboardAxisTicks, blackboardCountTicks } from './blackboardAxis'
for (const width of [333,3500]) it(`keeps ticks on the same bounded time axis at ${width}px`,()=>{
 const ticks=blackboardAxisTicks(Date.parse('2026-10-29'),Date.parse('2026-11-05'),width)
 expect(ticks[0]).toBe('2026-10-29')
 expect(ticks.at(-1)).toBe('2026-11-05')
 if(width===333)expect(ticks.length).toBeLessThanOrEqual(4)
 expect(new Set(ticks).size).toBe(ticks.length)
 expect(ticks.length).toBeLessThanOrEqual(Math.max(2,Math.floor(width/65)))
})
it('labels a single observed day once',()=>{
 expect(blackboardAxisTicks(Date.parse('2026-11-02'),Date.parse('2026-11-02'),333)).toEqual(['2026-11-02'])
})

it.each([0,1,2,3,149,150])('uses distinct integer count ticks for a peak of %i cards',maximum=>{
 const ticks=blackboardCountTicks(maximum)
 expect(ticks[0]).toBe(0)
 expect(ticks.at(-1)).toBe(Math.max(1,maximum))
 expect(new Set(ticks).size).toBe(ticks.length)
 expect(ticks.every(Number.isInteger)).toBe(true)
})
