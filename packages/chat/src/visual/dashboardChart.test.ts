import {expect, it} from 'vitest'
import {dashboardChart} from './dashboardChart'
it('uses the same clock format and horizontal labels without mutating authored specs', () => {
  const spec = {layer: [{mark: 'line', encoding: {x: {field: 't', type: 'temporal'}}}, {mark: 'bar', encoding: {x: {field: 'day', type: 'ordinal'}}}]}
  const copy = JSON.stringify(spec)
  const rendered = JSON.parse(dashboardChart(spec, []))
  expect(rendered.layer[0].encoding.x.axis.format).toBe('%H:%M')
  expect(rendered.layer[1].encoding.x.axis.labelAngle).toBe(0)
  expect(rendered.layer[1].encoding.x.axis.labelOverlap).toBe(true)
  expect(JSON.stringify(spec)).toBe(copy)
})
it('shows dates when the temporal domain crosses days', () => {
 const spec={mark:'line',encoding:{x:{field:'t',type:'temporal'}}}
 const rendered=JSON.parse(dashboardChart(spec,[{t:'2026-11-01T23:00:00Z'},{t:'2026-11-02T01:00:00Z'}]))
 expect(rendered.encoding.x.axis.format).toBe('%b %d')
})
it('uses the visible fixed time domain when retained rows span additional days', () => {
 const spec={mark:'line',encoding:{x:{field:'t',type:'temporal',scale:{domain:['2026-11-02T10:00:00Z','2026-11-02T12:00:00Z']}}}}
 const rendered=JSON.parse(dashboardChart(spec,[{t:'2026-11-01T23:00:00Z'},{t:'2026-11-02T01:00:00Z'}]))
 expect(rendered.encoding.x.axis.format).toBe('%H:%M')
})

it('restores dashboard guide defaults for runtime fallback while keeping native input unmodified',()=>{
 const spec={mark:'line',encoding:{x:{field:'second',type:'quantitative'},y:{field:'latency',type:'quantitative'}}}
 const native=JSON.parse(dashboardChart(spec,[{second:0,latency:1}]))
 expect(native.encoding.x.axis).toBeUndefined()
 const fallback=JSON.parse(dashboardChart(native,native.data.values,true))
 expect(fallback.encoding.x.axis).toEqual({labelAngle:0,labelOverlap:true,labelPadding:8})
 expect(spec.encoding.x).not.toHaveProperty('axis')
})
