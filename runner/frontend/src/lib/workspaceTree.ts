export type WorkspaceCode = 'M' | 'A' | 'U' | 'D' | 'C' | 'T' | 'R'
export type PathStatus = { path: string; code: WorkspaceCode; directory?: boolean }
export type TreeStatus = { branch: string; head?: string; changed: number; untracked: number; partial: boolean; files?: PathStatus[] }
export type TreeEntry = { name: string; path: string; directory: boolean; repository?: boolean; member_id?: string; deleted?: boolean }
export const statusLabels: Record<WorkspaceCode, string> = { M: 'Modified', A: 'Added', U: 'Untracked', D: 'Deleted', C: 'Conflict', T: 'Type changed', R: 'Renamed' }
export function workspaceChanges(statuses: Record<string, TreeStatus | null>) {
  return Object.entries(statuses).flatMap(([root, status]) => (status?.files ?? []).map(file => ({ ...file, path: root ? `${root}/${file.path}` : file.path })))
}
export function pathCodes(entry: TreeEntry, changes: PathStatus[]): WorkspaceCode[] {
  const codes = changes.filter(file => file.path === entry.path || (entry.directory && file.path.startsWith(`${entry.path}/`)) || (file.directory && entry.path.startsWith(`${file.path}/`))).map(file => file.code)
  return (['C', 'M', 'A', 'U', 'D', 'T', 'R'] as WorkspaceCode[]).filter(code => codes.includes(code))
}
/** Deleted paths have no directory entry; retain them and any missing parents as display rows. */
export function withDeletedEntries(entries: TreeEntry[], directory: string, changes: PathStatus[]): TreeEntry[] {
  const result = [...entries]
  for (const change of changes) {
    if (change.code !== 'D' || (directory && !change.path.startsWith(`${directory}/`))) continue
    const relative = directory ? change.path.slice(directory.length + 1) : change.path
    const [name, ...rest] = relative.split('/')
    if (name && !result.some(entry => entry.name === name)) result.push({ name, path: directory ? `${directory}/${name}` : name, directory: rest.length > 0, deleted: true })
  }
  return result
}
export function fileGlyphView(name: string): string {
  const extension = name.split('.').at(-1)?.toLowerCase() ?? ''
  if (['md', 'mdx', 'markdown'].includes(extension)) return 'markdown'
  if (['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp'].includes(extension)) return 'image'
  if (['html', 'htm'].includes(extension)) return 'html'
  if (['mp3', 'wav', 'ogg'].includes(extension)) return 'audio'
  if (['mp4', 'webm'].includes(extension)) return 'video'
  if (['patch', 'diff'].includes(extension)) return 'diff'
  return ['json', 'csv', 'pdf'].includes(extension) ? extension : 'text'
}

/** Compact only proven single-folder listings. Partial, empty and unread pages cannot prove a
 * chain. Repository and member roots keep standalone rows so their badges remain visible. */
export function compactFolderChain(head: TreeEntry, pages: Record<string, { entries: TreeEntry[]; truncated: boolean }>, memberRoots: ReadonlySet<string>, enabled: boolean): TreeEntry[] {
  const chain = [head]
  if (!enabled || !head.directory || head.deleted || head.repository || memberRoots.has(head.path)) return chain
  let tail = head
  while (true) {
    const page = pages[tail.path]
    if (!page || page.truncated || page.entries.length !== 1) break
    const child = page.entries[0]
    if (!child.directory || child.deleted || child.repository || memberRoots.has(child.path) || !child.path.startsWith(`${tail.path}/`)) break
    chain.push(child)
    tail = child
  }
  return chain
}
