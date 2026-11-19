import { expect, it } from 'vitest'
import { graphRows, releaseRanges, unmergedRefs } from './branchGraph'

it('keeps main straight and joins both merge parents', () => {
  const rows = graphRows({ refs: [{ name: 'main', sha: 'merge' }], more: false, commits: [
    { sha: 'merge', parents: ['main', 'side'], subject: 'Merge' },
    { sha: 'side', parents: ['root'], subject: 'Work' },
    { sha: 'main', parents: ['root'], subject: 'Main' },
    { sha: 'root', parents: [], subject: 'Root' },
  ] })
  expect(rows.map(r => r.lane)).toEqual([0, 1, 0, 0])
  expect(rows[0].edges).toEqual([{ from: 0, to: 0 }, { from: 0, to: 1 }])
  expect(rows[1].edges).toContainEqual({ from: 1, to: 0 })
})

it('reserves the first lane for the declared default branch', () => {
  const rows = graphRows({ default_branch: 'trunk', refs: [{ name: 'trunk', sha: 'base' }], commits: [{ sha: 'worker', parents: ['base'], subject: 'Work' }, { sha: 'base', parents: [], subject: 'Base' }], more: false })
  expect(rows[1].lane).toBe(0)
  expect(rows[0].lane).toBe(1)
})

it('keeps connected lanes stable when history grows', () => {
 const commits = [
  { sha: 'merge', parents: ['main', 'a'], subject: 'Merge' },
  { sha: 'a', parents: ['root'], subject: 'A' },
  { sha: 'main', parents: ['root'], subject: 'Main' },
  { sha: 'b', parents: ['root'], subject: 'B' },
 ]
 const graph = { default_branch: 'trunk', refs: [{ name: 'trunk', sha: 'merge' }, { name: 'feature/a', sha: 'a' }, { name: 'feature/b', sha: 'b' }], commits, more: true }
 const rows = graphRows(graph)
 expect(rows.map(row => row.lane)).toEqual([0, 1, 0, 2])
 const extended = graphRows({ ...graph, commits: [...commits, { sha: 'root', parents: [], subject: 'Root' }] })
 expect(extended.slice(0, rows.length).map(row => row.lane)).toEqual(rows.map(row => row.lane))
})

it('caps dense history at four columns and reuses finished columns', () => {
  const commits = Array.from({ length: 50 }, (_, i) => ({ sha: String(i), parents: [], subject: 'Independent history' }))
  const rows = graphRows({ refs: [], commits, more: false })
  expect(rows.every(row => row.width <= 4)).toBe(true)
  expect(new Set(rows.map(row => row.lane)).size).toBe(1)
})

it('hides only proven merged refs, retaining main, open and unknown tips', () => {
  const graph = { refs: [{ name: 'main', sha: 'merge' }, { name: 'merged', sha: 'side' }, { name: 'open', sha: 'open' }, { name: 'unknown', sha: 'missing' }], more: true, commits: [
    { sha: 'merge', parents: ['base', 'side'], subject: 'Merge' },
    { sha: 'side', parents: ['base'], subject: 'Side' },
    { sha: 'open', parents: ['base'], subject: 'Open' },
  ] }
  expect(unmergedRefs(graph).map(ref => ref.name)).toEqual(['main', 'open', 'unknown'])
})

it('shows only release ranges whose boundaries are known, including ordinary commits', () => {
  const graph = { refs: [], more: true, commits: [
    { sha: 'new', parents: ['work'], subject: 'Release' },
    { sha: 'work', parents: ['old'], subject: 'Ordinary work' },
    { sha: 'old', parents: ['missing'], subject: 'Earlier release' },
  ], tags: [
    { name: 'v2', sha: 'new', previous: 'v1', contains: ['new', 'work', 'old'], complete: false },
    { name: 'v1', sha: 'old', contains: ['old'], complete: false },
    { name: 'v0', sha: 'missing', contains: [], complete: false },
  ] }
  expect(releaseRanges(graph).map(range => [range.tag.name, range.commits.map(commit => commit.sha)])).toEqual([['v2', ['new', 'work']]])
  expect(releaseRanges({ ...graph, commits: graph.commits.filter(commit => commit.sha !== 'work') })).toEqual([])
})

it('caps simultaneous branches without dropping a parent connection', () => {
  const commits = [...Array.from({ length: 30 }, (_, i) => ({ sha: String(i), parents: ['root'], subject: 'Work' })), { sha: 'root', parents: [], subject: 'Root' }]
  const rows = graphRows({ refs: commits.map(commit => ({ name: commit.sha, sha: commit.sha })), commits, more: false })
  expect(Math.max(...rows.map(row => row.width))).toBe(4)
  expect(rows.slice(0, 30).every(row => row.edges.length === 1 && row.edges[0].to === rows[30].lane)).toBe(true)
})
