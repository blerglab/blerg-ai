import {expect,it} from 'vitest'
import {VISUAL_TEMPLATES,templateDashboard} from './templates'
import {parseDashboard} from './dashboard'
import {dashboardChart} from './dashboardChart'
import {getChartModel} from './render'
import {renderBlergChart} from './blerg/render'
import {readVisualTheme} from './theme'
import {classifyChart} from './blerg/classify'
it('ships the catalog as four strict dashboard card kinds with native chart forms',()=>{
 const dashboard=parseDashboard(JSON.stringify(templateDashboard()))
 expect(new Set(dashboard.cards.map(card=>card.kind))).toEqual(new Set(['chart','stat','gauge','text']))
 expect(VISUAL_TEMPLATES).toHaveLength(10)
 for(const template of VISUAL_TEMPLATES){
  expect(template.summary).toBeTruthy();expect(template.empty).toContain('No data');expect(template.freshness).toBeTruthy()
  const card=template.card
  if(card.kind==='chart' && card.data.kind==='inline'){
   const spec=getChartModel(dashboardChart(card.spec!,card.data.rows))
   expect(classifyChart(spec),template.id).toBe('blerg')
   const host=document.createElement('div')
   expect(()=>{const visual=renderBlergChart(spec,readVisualTheme(host),host,300,180);visual.dispose()},template.id).not.toThrow()
  }
 }
})
