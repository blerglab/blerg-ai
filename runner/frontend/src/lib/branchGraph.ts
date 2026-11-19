export interface BranchGraph {
  default_branch?: string
  member_id?: string
  generation?: number
  launch_generation?: string
  commits: { sha: string; parents: string[]; subject: string; date?: string }[]
  refs: { name: string; sha: string }[]
  tags?: { name: string; sha: string; contains: string[]; previous?: string; complete: boolean }[]
  tags_limited?: boolean
  refs_limited?: boolean
  more: boolean
}

// Only positive ancestry proof hides a ref. Missing history stays visible.
export function unmergedRefs(graph: BranchGraph) {
  const byId = new Map(graph.commits.map(commit => [commit.sha, commit]))
  const primary = graph.refs.find(ref => ref.name === (graph.default_branch ?? 'main'))
  const merged = new Set<string>()
  const queue = primary ? [primary.sha] : []
  while (queue.length) {
    const sha = queue.pop()!
    if (merged.has(sha)) continue
    merged.add(sha)
    queue.push(...(byId.get(sha)?.parents ?? []))
  }
  return graph.refs.filter(ref => ref === primary || !merged.has(ref.sha))
}

// Prove a release range up to its preceding tag, rather than requiring the
// entire repository back to its root. Every open boundary makes it unknown.
export function releaseRanges(graph: BranchGraph) {
  const byId = new Map(graph.commits.map(commit => [commit.sha, commit]))
  return (graph.tags ?? []).flatMap(tag => {
    const previous = graph.tags?.find(other => other.name === tag.previous)
    const excluded = new Set(previous?.contains ?? [])
    if (previous) excluded.add(previous.sha)
    const landed = new Set<string>()
    const queue = [tag.sha]
    while (queue.length) {
      const sha = queue.pop()!
      if (excluded.has(sha) || landed.has(sha)) continue
      const commit = byId.get(sha)
      if (!commit) return []
      landed.add(sha)
      queue.push(...commit.parents)
    }
    return landed.size ? [{ tag, previous, commits: graph.commits.filter(commit => landed.has(commit.sha)) }] : []
  })
}

// First-parent chains stay continuous. Reuse columns once a chain has joined
// its parent; dense concurrent history shares the fourth column.
export function graphRows(graph: BranchGraph) {
  const byId = new Map(graph.commits.map(commit => [commit.sha, commit]))
  const assigned = new Map<string, number>()
  let nextLane = 1
  const seed = (sha: string, lane: number) => {
    while (!assigned.has(sha)) {
      const commit = byId.get(sha)
      if (!commit) break
      assigned.set(sha, lane)
      if (!commit.parents[0]) break
      sha = commit.parents[0]
    }
  }
  const primary = graph.refs.find(ref => ref.name === (graph.default_branch ?? 'main'))
  if (primary) seed(primary.sha, 0)
  for (const ref of [...graph.refs].sort((a, b) => a.name.localeCompare(b.name))) {
    if (!assigned.has(ref.sha)) seed(ref.sha, nextLane++)
  }
  for (const commit of graph.commits) if (!assigned.has(commit.sha)) seed(commit.sha, nextLane++)
  const ends = [Infinity, -1, -1, -1]
  const columns = new Map<number, number>([[0, 0]])
  const indexes = new Map(graph.commits.map((commit, i) => [commit.sha, i]))
  for (const [index, commit] of graph.commits.entries()) {
    const chain = assigned.get(commit.sha)!
    if (columns.has(chain)) continue
    const column = [1, 2, 3].find(lane => ends[lane] < index) ?? 3
    columns.set(chain, column)
    let end = index
    for (const [i, candidate] of graph.commits.entries()) {
      if (assigned.get(candidate.sha) !== chain) continue
      end = Math.max(end, i, ...candidate.parents.map(parent => indexes.get(parent) ?? graph.commits.length))
    }
    ends[column] = Math.max(ends[column], end)
  }
  const width = Math.max(0, ...columns.values()) + 1
  return graph.commits.map(commit => {
    const lane = columns.get(assigned.get(commit.sha)!)!
    return { commit, lane, width, edges: commit.parents.map(parent => ({ from: lane, to: columns.get(assigned.get(parent)!) ?? lane })) }
  })
}
