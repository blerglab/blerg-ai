import {expect,it} from 'vitest'
import {liveFileSupport} from './liveFile'
import {VISUAL_TEMPLATES,templateDashboard} from './templates'
it('admits only native templates with a real data adapter for live files',()=>{
 for(const template of VISUAL_TEMPLATES){
  if(template.card.kind!=='chart'||template.card.data.kind!=='inline')continue
  expect(liveFileSupport('vega-lite',JSON.stringify({...template.card.spec,data:{values:template.card.data.rows}})),template.id).toBe(template.renderer==='native'?'native':'unsupported-live')
 }
 expect(liveFileSupport('dashboard',JSON.stringify(templateDashboard()))).toBe('unsupported-live')
 const dashboard=templateDashboard();dashboard.cards=dashboard.cards.filter(card=>card.id!=='distribution'&&card.id!=='stat-sparkline')
 expect(liveFileSupport('dashboard',JSON.stringify(dashboard))).toBe('native')
 expect(liveFileSupport('vega-lite','not JSON')).toBe('unsupported-live')
 expect(liveFileSupport(null,'ordinary text')).toBe('file')
})
