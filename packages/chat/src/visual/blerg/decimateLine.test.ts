import {expect,it} from 'vitest'
import {decimateLine} from './marks/line'
it('bounds dense paint geometry while retaining endpoints, order and spikes',()=>{
 const points=Array.from({length:10000},(_,x)=>({x,y:x===5000?100:x===5001?-100:0}))
 const painted=decimateLine(points,1024)
 expect(painted.length).toBeLessThanOrEqual(1024)
 expect(painted[0]).toBe(points[0]);expect(painted.at(-1)).toBe(points.at(-1))
 expect(painted).toContain(points[5000]);expect(painted).toContain(points[5001])
 expect(painted.every((p,i)=>i===0||p.x>painted[i-1].x)).toBe(true)
 expect(points).toHaveLength(10000)
})
it('preserves stacked area baseline extrema as well as top extrema within the same budget',()=>{
 const points=Array.from({length:10000},(_,x)=>({x,y:20,base:x===5000?19:x===5001?-30:0}))
 const painted=decimateLine(points,1024)
 expect(painted).toContain(points[5000]);expect(painted).toContain(points[5001])
 expect(painted.length).toBeLessThanOrEqual(1024)
})
