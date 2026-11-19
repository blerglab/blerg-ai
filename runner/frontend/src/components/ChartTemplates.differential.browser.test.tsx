import '@blerglab/chat/blerg.css'
import {expect,it} from 'vitest'
import {compileVegaViewForDifferentialTesting} from '../../../../packages/chat/src/visual/vegaRuntime'
import {renderBlergChart} from '../../../../packages/chat/src/visual/blerg/render'
import {heatmapRange,vegaTheme,readVisualTheme} from '../../../../packages/chat/src/visual/theme'

it('status intervals preserve Vega temporal duration and gap ratios and category colors',async()=>{
 const rows=[{id:'a',worker:'A',start:'2026-11-04T09:00:00Z',end:'2026-11-04T10:00:00Z',status:'Busy'},{id:'b',worker:'A',start:'2026-11-04T11:00:00Z',end:'2026-11-04T13:00:00Z',status:'Idle'}]
 const spec={mark:'rect',data:{values:rows},encoding:{x:{field:'start',type:'temporal'},x2:{field:'end',type:'temporal'},y:{field:'worker',type:'ordinal'},color:{field:'status',type:'nominal'}}}
 const host=document.createElement('div'),theme=readVisualTheme(host),visual=renderBlergChart(spec,theme,host,640,360)
 const {view}=await compileVegaViewForDifferentialTesting({...spec,width:640,height:360},theme)
 try {
  const xs=view.scale('x') as (time:number)=>number,colors=view.scale('color') as (key:string)=>string
  const marks=[...host.querySelectorAll('[data-datum-key] rect')]
  const span=(i:number)=>xs(Date.parse(rows[i].end))-xs(Date.parse(rows[i].start))
  expect(Number(marks[1].getAttribute('width'))/Number(marks[0].getAttribute('width'))).toBeCloseTo(span(1)/span(0),6)
  const gap=Number(marks[1].getAttribute('x'))-Number(marks[0].getAttribute('x'))-Number(marks[0].getAttribute('width'))
  expect(gap/Number(marks[0].getAttribute('width'))).toBeCloseTo((xs(Date.parse(rows[1].start))-xs(Date.parse(rows[0].end)))/span(0),6)
  rows.forEach((row,i)=>expect(marks[i].getAttribute('fill')).toBe(colors(row.status)))
 }finally{view.finalize();visual.dispose()}
})

it('histogram geometry preserves Vega pre-binned width and count ratios',async()=>{
 const rows=[{lower:0,upper:10,count:2},{lower:10,upper:30,count:4}]
 const spec={mark:'bar',data:{values:rows},encoding:{x:{field:'lower',type:'quantitative',bin:'binned'},x2:{field:'upper',type:'quantitative'},y:{field:'count',type:'quantitative'}}}
 const host=document.createElement('div'),theme=readVisualTheme(host),visual=renderBlergChart(spec,theme,host,640,360)
 const {view}=await compileVegaViewForDifferentialTesting({...spec,width:640,height:360},theme)
 try {
  const xs=view.scale('x') as (n:number)=>number,ys=view.scale('y') as (n:number)=>number
  const marks=[...host.querySelectorAll('[data-datum-key]')]
  const width=(i:number)=>xs(rows[i].upper)-xs(rows[i].lower),height=(i:number)=>ys(0)-ys(rows[i].count)
  expect(Number(marks[1].getAttribute('width'))/Number(marks[0].getAttribute('width'))).toBeCloseTo(width(1)/width(0),6)
  expect(Number(marks[1].getAttribute('height'))/Number(marks[0].getAttribute('height'))).toBeCloseTo(height(1)/height(0),6)
 }finally{view.finalize();visual.dispose()}
})

const canonical=(color:string)=>{const ctx=document.createElement('canvas').getContext('2d')!;ctx.fillStyle=color;return String(ctx.fillStyle)}
const luminance=(color:string)=>{
 const parts=color.slice(1).match(/../g)!.map(part=>parseInt(part,16)/255).map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4)
 return parts[0]*.2126+parts[1]*.7152+parts[2]*.0722
}
const contrast=(a:string,b:string)=>{const x=luminance(a),y=luminance(b);return (Math.max(x,y)+.05)/(Math.min(x,y)+.05)}
for(const name of ['dark','light','colorblind-dark','colorblind-light'])it(`visible sequential heatmap ramp and Vega parity in ${name}`,async()=>{
 const previous=document.documentElement.dataset.theme
 document.documentElement.dataset.theme=name
 const host=document.createElement('div');document.body.append(host)
 const theme=readVisualTheme(host)
 const rows=Array.from({length:11},(_,x)=>({x:String(x),y:'One',count:x}))
 const spec={mark:'rect',data:{values:rows},encoding:{x:{field:'x',type:'nominal'},y:{field:'y',type:'nominal'},color:{field:'count',type:'quantitative'}}}
 const visual=renderBlergChart(spec,theme,host,390,300)
 try{
  expect(vegaTheme(theme).range.heatmap).toEqual([...heatmapRange(theme)])
  for(const cell of host.querySelectorAll('[data-datum-key]'))expect(contrast(cell.getAttribute('fill')!,theme.surface2)).toBeGreaterThanOrEqual(3)
  const {view}=await compileVegaViewForDifferentialTesting({...spec,width:390,height:300},theme)
  try{
   const scale=view.scale('color') as (n:number)=>string
   expect(canonical(scale(0))).toBe(heatmapRange(theme)[0])
   expect(canonical(scale(10))).toBe(heatmapRange(theme)[1])
  }finally{view.finalize()}
 }finally{visual.dispose();host.remove();if(previous)document.documentElement.dataset.theme=previous;else delete document.documentElement.dataset.theme}
})
