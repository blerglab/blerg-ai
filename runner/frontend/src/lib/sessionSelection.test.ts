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
  ['',null,'cron',['cluster','cron','host']], ['',null,'ended',['cluster','host']],
 ] as const) {
  const expanded=selectSessionSections(sessions,query,place,current)
  const tiles=orderedTiles(sessions,query,place,current)
  expect(tiles.map(s=>s.id).sort()).toEqual([...expected].sort())
  expect(tiles).toEqual([...expanded.starred,...expanded.listed])
 }
})

it.each(['stopped', 'ended'] as const)('keeps a viewed %s coordinator and its children in History', status => {
  const parent = makeSession({ id: 'parent', status })
  const child = makeSession({ id: 'child', parent_session_id: parent.id, status: 'ended' })
  for (const current of [parent.id, child.id]) {
    const sections = selectSessionSections([parent, child], '', null, current)
    expect(sections.main).toEqual([])
    expect(sections.done).toEqual([parent])
    expect(sections.tree.childrenOf.get(parent.id)).toEqual([child])
  }
})

it('keeps a viewed ended automated session in its existing section', () => {
  const session = makeSession({ id: 'automated', status: 'ended', cron_id: 'nightly' })
  const sections = selectSessionSections([session], '', null, session.id)
  expect(sections.main).toEqual([])
  expect(sections.automated).toEqual([session])
})
