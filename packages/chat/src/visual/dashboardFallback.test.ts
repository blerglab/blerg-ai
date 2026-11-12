import {expect,it,vi} from 'vitest'
import {renderVisual} from './render'
import {readVisualTheme} from './theme'
const fallback=vi.hoisted(()=>vi.fn(async()=>({svg:'<svg/>',width:390,height:300,dispose(){}})))
vi.mock('./blerg/render',()=>({renderBlergChart:()=>{throw new Error('Unsupported native rows')}}))
vi.mock('./vega',()=>({renderVegaLite:fallback}))
it('keeps dashboard guide defaults when a native scene falls back at runtime',async()=>{
 const host=document.createElement('div'),theme=readVisualTheme(host)
 const source=JSON.stringify({mark:'bar',data:{values:[{day:'Mon',count:2}]},encoding:{x:{field:'day',type:'nominal'},y:{field:'count',type:'quantitative'}}})
 const warning=vi.spyOn(console,'warn').mockImplementation(()=>{})
 try{
  await renderVisual('vega-lite',source,host,theme,undefined,{available:{width:390,height:300},fill:true,dashboardGuides:true})
  expect(fallback).toHaveBeenCalledWith(expect.objectContaining({encoding:expect.objectContaining({x:expect.objectContaining({axis:{labelAngle:0,labelOverlap:true,labelPadding:8}})})}),host,theme,undefined,expect.objectContaining({dashboardGuides:true}))
 }finally{warning.mockRestore()}
})
