import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'
import type { PaletteItem, PaletteProvider } from './registry'

type WorkspaceReply = {
  result: { ok: boolean; entries: { path: string; name: string; directory: boolean; member_id?: string }[]; truncated: boolean; unavailable_members?: string[]; launch_generation: string; generation: number }
  members: { id: string; name: string }[]
}
const bindingOf = (reply: WorkspaceReply) => `${reply.result.launch_generation}:${reply.result.generation}`

/** P4 owns traversal, aggregate budgets, protected names, authorization and runtime fencing. */
export function workspaceProvider(sessionId: string, openFile: (path: string) => void): PaletteProvider {
  let recent: PaletteItem[] = []
  let heldFor = signedInAccount()
  let binding = ''
  return {
    id: `workspace:${sessionId}`, mode: 'files',
    async search(text, signal) {
      const account = signedInAccount()
      if (heldFor !== account) { recent = []; binding = ''; heldFor = account }
      const filename = text.trim().split('/').pop() ?? ''
      if (!filename) return { items: recent, note: 'Type a filename to search across repositories. Recent files appear first.' }
      async function request(filter: string) {
        const query = new URLSearchParams({ op: 'search', path: '', filter })
        const response = await apiFetch(`/api/sessions/${encodeURIComponent(sessionId)}/workspace?${query}`, { signal })
        if (!response.ok) {
          if (response.status === 404) throw new Error('Workspace search is unavailable for this session.')
          if (response.status === 403) throw new Error('You do not have permission to search this workspace.')
          throw new Error('Workspace search is unavailable or busy. Try again.')
        }
        const reply = await response.json() as WorkspaceReply
        signal.throwIfAborted()
        if (signedInAccount() !== account) throw new Error('Your sign-in changed. Open search again.')
        if (!reply.result?.ok || !Array.isArray(reply.result.entries) || !Array.isArray(reply.members)) throw new Error('The workspace could not be safely searched. Try again.')
        return reply
      }
      // P4 requires nonempty substrings. Exact candidates let a longer filename narrow a
      // truncated broad search. A second bounded search retains filename subsequence matches.
      // Never fan out per repository, paginate, or combine different runtime generations.
      const exact = await request(filename)
      const seed = [...filename][0]
      const fuzzy = seed === filename ? exact : await request(seed)
      const generation = bindingOf(fuzzy)
      if (binding && binding !== generation) recent = []
      binding = generation
      if (bindingOf(exact) !== generation) throw new Error('The workspace changed. Search again.')
      const candidates = [...new Map([...exact.result.entries, ...fuzzy.result.entries].filter(entry => !entry.directory).map(entry => [entry.path, entry])).values()]
      return {
        items: candidates.slice(0, 200).map(entry => {
          const item: PaletteItem = {
            id: `file:${sessionId}:${generation}:${entry.path}`, title: entry.path,
            detail: fuzzy.members.find(member => member.id === entry.member_id)?.name,
            shortcut: 'Enter', run: () => {
              if (signedInAccount() !== account) return
              recent = [item, ...recent.filter(previous => previous.id !== item.id)].slice(0, 20)
              openFile(entry.path)
            },
          }
          return item
        }),
        note: [exact.result.truncated || fuzzy.result.truncated || candidates.length > 200 ? 'Partial results: the workspace search limit was reached.' : '', exact.result.unavailable_members?.length || fuzzy.result.unavailable_members?.length ? 'Some repositories are unavailable.' : ''].filter(Boolean).join(' '),
      }
    },
  }
}
