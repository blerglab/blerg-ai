import {expect,it} from 'vitest'
import {isScaleOptions,positionDomain,positionScale,temporalTicks,temporalValue,formatTemporalTick} from './scaleOptions'
it('honors zero, exact domains, reversed domains and nice',()=>{
 expect(positionDomain([185.37,190.1],{zero:false},true)).toEqual([185,190.5])
 expect(positionDomain([185,190],{domain:[181,199],nice:false},true)).toEqual([181,199])
 expect(positionDomain([185,190],{domain:[199,181],nice:false},true)).toEqual([199,181])
 expect(positionDomain([185,190],{domain:[181,199],nice:true},true)).toEqual([180,200])
 expect(positionDomain([185,190],undefined,true)).toEqual([0,200])
})
it('uses local calendar ticks and formatting without a zero epoch',()=>{
 const start=temporalValue('2026-11-05T09:30:00'),end=temporalValue('2026-11-05T12:21:00')
 const domain=positionDomain([start,end],undefined,false,true)
 expect(domain).toEqual([start,end])
 const ticks=temporalTicks(domain,6)
 expect(ticks.map(x=>formatTemporalTick(x,'%H:%M'))).toEqual(['09:30','10:00','10:30','11:00','11:30','12:00'])
 expect(positionDomain([start,end],{nice:true},false,true)[1]).toBeGreaterThan(end)
})
it('refuses unsupported scale settings and invalid domains',()=>{
 for(const value of [{type:'log'},{domain:[1]},{domain:[1,1]},{nice:5},{zero:null},{domain:[NaN,2]}])expect(isScaleOptions(value)).toBe(false)
 expect(isScaleOptions({zero:true},true)).toBe(false)
 expect(isScaleOptions({domain:['2026-11-05','2026-11-06']},true)).toBe(true)
})
it('keeps explicit domains exact by default and still honors explicit zero',()=>{
 expect(positionDomain([185,190],{domain:[181,199]},true)).toEqual([181,199])
 expect(positionDomain([185,190],{domain:[181,199],zero:true,nice:false},true)).toEqual([0,199])
 expect(positionDomain([185,190],{domain:[199,181],zero:true},true)).toEqual([0,181])
})
it('preserves singleton domains for midpoint mapping, matching Vega',()=>{
 expect(positionDomain([185],{zero:false},true)).toEqual([185,185])
 expect(positionScale([185,185],[300,0])(185)).toBe(150)
 const time=temporalValue('2026-11-05T09:30:00')
 expect(positionDomain([time],undefined,false,true)).toEqual([time,time])
})
