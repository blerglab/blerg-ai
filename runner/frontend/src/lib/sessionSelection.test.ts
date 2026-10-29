import { expect, it } from 'vitest'
import { selectSessionSections } from './sessionSelection'
import { orderedTiles } from './sessionTiles'
import { makeSession } from '../test/fixtures'

it('shares exactly the main section with tiles across active, automated, ended and child sessions, place and search', () => {
 const sessions = [
  makeSession({id:'host',title:'Build widgets',runtime:'daemon',daemon_id:'desktop',status:'idle'}),
  makeSession({id:'cluster',title:'Cluster widgets',runtime:'cluster',status:'running'}),
  makeSession({id:'cron',title:'Nightly widgets',cron_id:'nightly',status:'running'}),
  makeSession({id:'ended',title:'Previous widgets',status:'stopped'}),
  makeSession({id:'error',title:'Failed widgets',status:'error'}),
  makeSession({id:'child',title:'Nested check',parent_session_id:'host',status:'running'}),
 ]
 for (const [query,place,current,expected] of [
  ['',null,undefined,['cluster','host']], ['', 'cluster',undefined,['cluster']], ['', 'desktop',undefined,['host']],
  ['Nested',null,undefined,['host']], ['widgets','desktop',undefined,['cluster','host']],
  ['',null,'cron',['cluster','cron','host']], ['',null,'ended',['cluster','ended','host']],
 ] as const) {
  const expanded=selectSessionSections(sessions,query,place,current)
  const tiles=orderedTiles(sessions,query,place,current)
  expect(tiles.map(s=>s.id).sort()).toEqual([...expected].sort())
  expect(tiles).toEqual([...expanded.starred,...expanded.listed])
 }
})
