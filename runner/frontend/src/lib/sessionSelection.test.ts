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

it('counts only History workspaces and nests archived workers, including search matches', () => {
  const archived = makeSession({ id: 'archive', title: 'Archived workspace', status: 'ended', archived_at: '2026-01-01' })
  const worker = makeSession({ id: 'worker', title: 'Needle worker', status: 'ended', parent_session_id: archived.id, archived_at: '2026-01-01' })
  const live = makeSession({ id: 'live', status: 'running' })
  const liveWorker = makeSession({ id: 'live-worker', status: 'ended', parent_session_id: live.id, archived_at: '2026-01-01' })
  const orphan = makeSession({ id: 'orphan', status: 'ended', parent_session_id: 'missing', archived_at: '2026-01-01' })
  const finished = makeSession({ id: 'finished', status: 'ended' })
  const finishedWorker = makeSession({ id: 'finished-worker', status: 'ended', parent_session_id: finished.id, archived_at: '2026-01-01' })
  const sessions = [archived, worker, live, liveWorker, orphan, finished, finishedWorker]
  const sections = selectSessionSections(sessions, '', null)
  expect(sections.done.map(s => s.id).sort()).toEqual(['archive', 'finished', 'orphan'])
  expect(sections.tree.childrenOf.get('archive')).toEqual([worker])
  expect(sections.tree.childrenOf.get('finished')).toEqual([finishedWorker])
  const searched = selectSessionSections(sessions, 'needle', null)
  expect(searched.done).toEqual([archived])
  expect(searched.tree.childrenOf.get('archive')).toEqual([worker])
})

it('keeps archived grandchildren out of a live workspace regardless of intermediate status', () => {
  const workspace = makeSession({ id: 'live-root', status: 'running' })
  const child = makeSession({ id: 'middle', parent_session_id: workspace.id, status: 'ended' })
  const grandchild = makeSession({ id: 'archived-grandchild', parent_session_id: child.id, status: 'error', archived_at: '2026-01-01' })
  const result = selectSessionSections([workspace, child, grandchild], '', null)
  expect(result.tree.childrenOf.get(child.id)).toBeUndefined()
  expect(result.done).toEqual([])
  expect(selectSessionSections([workspace, child, grandchild], 'archived-grandchild', null).done).toEqual([])
})
