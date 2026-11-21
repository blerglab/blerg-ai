import {expect,it} from 'vitest'
import {seriesLegend,isLegendOptions} from './seriesLegend'
import {readVisualTheme} from '../theme'
it('wraps a top legend before reserving the plot and uses theme colors',()=>{
 const theme=readVisualTheme(document.createElement('div')),labels=['Previous close','NVDA','A third series']
 const result=seriesLegend(labels,()=>theme.accent,200,theme,undefined,{orient:'top'})
 expect(result.right).toBe(0)
 expect(result.top).toBeGreaterThan(theme.fontSize*2)
 const svg=new DOMParser().parseFromString(`<svg>${result.svg(0,0)}</svg>`,'image/svg+xml')
 expect([...svg.querySelectorAll('text')].map(x=>x.textContent)).toEqual(labels)
 expect([...svg.querySelectorAll('line')].every(x=>x.getAttribute('stroke')===theme.accent)).toBe(true)
 expect(seriesLegend(labels,()=>theme.accent,200,theme,undefined,null).top).toBe(0)
 expect(isLegendOptions({orient:'left'})).toBe(false)
 expect(isLegendOptions({orient:'top',columns:4})).toBe(false)
})
