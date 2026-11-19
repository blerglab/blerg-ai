import {expect,it} from 'vitest'
import {page,commands} from 'vitest/browser'
import original from '../test/nvdaOriginalSpec.json'
import demo from '../test/nvdaSpec.json'
import '../index.css'
import '@blerglab/chat/blerg.css'
import {renderBlergChart} from '../../../../packages/chat/src/visual/blerg/render'
import {readVisualTheme} from '../../../../packages/chat/src/visual/theme'
import {compileVegaViewForDifferentialTesting} from '../../../../packages/chat/src/visual/vegaRuntime'

interface Scene {marktype?:string;role?:string;items?:Scene[];x?:number;y?:number;x2?:number;y2?:number;text?:string;stroke?:string}
function ruleItems(root:Scene):Scene[]{
 if(root.marktype==='rule' && root.role==='mark')return root.items ?? []
 return (root.items ?? []).flatMap(ruleItems)
}
for(const width of [390,3840])for(const themeId of ['dark','colorblind-dark'])it(`compares original endpoints and corrected horizontal reference directly with Vega: ${width} ${themeId}`,async()=>{
 await page.viewport(width,900)
 document.documentElement.dataset.theme=themeId
 const host=document.createElement('div');host.style.cssText=`width:${width}px;background:var(--chat-surface)`;document.body.append(host)
 try{
  const theme=readVisualTheme(host)
  for(const [name,spec] of [['original',original],['demo',demo]] as const){
   const visual=renderBlergChart(spec,theme,host,width,500)
   const {view}=await compileVegaViewForDifferentialTesting({...spec,width:640,height:360},theme)
   try{
    const root=host.querySelector<SVGSVGElement>('svg')!,[x,y,w,h]=JSON.parse(root.dataset.plotBounds!) as number[]
    const reference=[...root.querySelectorAll<SVGLineElement>('[data-reference-rule]')]
    const actual=ruleItems((view.scenegraph() as unknown as {root:Scene}).root).sort((a,b)=>(a.x ?? 0)-(b.x ?? 0))
    expect(reference).toHaveLength(name==='original' ? 2 : 1)
    expect(actual).toHaveLength(reference.length)
    const vw=Number(view.signal('width')),vh=Number(view.signal('height'))
    for(const [index,line] of reference.entries()){
     const toRoot=(a:string,b:string)=>new DOMPoint(Number(line.getAttribute(a)),Number(line.getAttribute(b))).matrixTransform(line.getScreenCTM()!).matrixTransform(root.getScreenCTM()!.inverse())
     const a=toRoot('x1','y1'),b=toRoot('x2','y2'),vega=actual[index]
     const vx1=(vega.x ?? 0)/vw,vx2=(vega.x2 ?? vega.x ?? 0)/vw,vy1=(vega.y ?? 0)/vh,vy2=(vega.y2 ?? vega.y ?? 0)/vh
     // Vega's horizontal rule is encoded right-to-left; compare geometric
     // endpoints independent of drawing direction, in each plot's pixel frame.
     expect((Math.min(a.x,b.x)-x)/w).toBeCloseTo(Math.min(vx1,vx2),5)
     expect((Math.max(a.x,b.x)-x)/w).toBeCloseTo(Math.max(vx1,vx2),5)
     expect((Math.min(a.y,b.y)-y)/h).toBeCloseTo(Math.min(vy1,vy2),5)
     expect((Math.max(a.y,b.y)-y)/h).toBeCloseTo(Math.max(vy1,vy2),5)
     if(name==='demo'){
      expect(a.y).toBeCloseTo(b.y,5);expect(a.x).toBeCloseTo(x,5);expect(b.x).toBeCloseTo(x+w,5)
      expect(root.querySelector('[data-reference-label]')?.textContent).toBe('Previous close · $200.13')
     }else{
      // The authored x+y rule emits one vertical segment per endpoint, not a now rule.
      expect(a.x).toBeCloseTo(b.x,5)
      expect(line.getAttribute('stroke')).toBe(theme.palette[1])
     }
    }
    if(name==='demo'){
     await commands.startChartCapture(`temporal-${themeId}-${width}`,false)
     const preview=window.frameElement!.parentElement as HTMLElement,transform=preview.style.transform
     preview.style.transform='none'
     try{
      expect(host.getBoundingClientRect().width).toBeCloseTo(width,0)
      await commands.chartScreenshot(`temporal-${themeId}-${width}`,width,600)
     }finally{preview.style.transform=transform;await commands.stopChartCapture([])}
     const surface=host.querySelector('svg'),next=structuredClone(spec)
     const data=next.layer[1].data as {values:{value:number}[]};data.values[0].value=180
     expect(visual.applyData!(next,1)).toBe(true);visual.snapshot!()
     expect(host.querySelector('svg')).toBe(surface)
    }
   }finally{visual.dispose();view.finalize()}
  }
 }finally{host.remove();delete document.documentElement.dataset.theme}
})
