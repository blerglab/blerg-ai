import { apiFetch } from '../apiFetch'
import { signedInAccount } from '../lib/liveBoardCard'
import type { Ticket } from '@blerglab/chat'
import type { PaletteItem, PaletteProvider, SearchResults } from './registry'

/** Board discovery and search both use the person's own sign-in. No held results. */
export function cardProvider(open: (ticket: Ticket) => void): PaletteProvider {
  return {
    id: 'cards', mode: 'cards',
    async search(query, signal) {
      if (!query.trim()) return { items: [], note: 'Type a card number or title.' }
      const account = signedInAccount()
      const response = await apiFetch('/api/session-boards', { signal })
      if (!response.ok) throw new Error('Could not load boards. Try again.')
      const reply = await response.json() as { boards?: { id: string; name: string }[] }
      signal.throwIfAborted()
      if (signedInAccount() !== account) throw new Error('Your sign-in changed. Open search again.')
      const boards = reply.boards ?? []
      const results: SearchResults[] = []
      let next = 0
      const searchBoard = async (board: { id: string; name: string }): Promise<SearchResults> => {
        const response = await apiFetch(`/api/board-cards/${encodeURIComponent(board.id)}/search?${new URLSearchParams({ q: query })}`, { signal })
        // Access may have changed since discovery; never reuse a previous result.
        if (response.status === 403 || response.status === 404) return { items: [], note: '' }
        if (!response.ok) return { items: [], note: 'Some boards could not be searched. Try again.' }
        const reply = await response.json() as { cards: { number: number; title: string; column_name?: string }[]; truncated?: boolean }
        const items = reply.cards.map(card => ({
          id: `card:${board.id}:${card.number}`, group: 'cards' as const, title: `#${card.number} ${card.title}`,
          detail: [board.name, card.column_name].filter(Boolean).join(' · '), searchMatched: true,
          run: () => {
            if (signedInAccount() === account) open({ source: 'blerg-board', board_id: board.id, key: `#${card.number}`, title: card.title, status: card.column_name })
          },
        } satisfies PaletteItem))
        return { items, note: reply.truncated ? 'Partial card results: narrow your search to see more.' : '' }
      }
      // Keep large accounts from starting hundreds of credential exchanges at once.
      await Promise.all(Array.from({ length: Math.min(4, boards.length) }, async () => {
        while (next < boards.length) {
          signal.throwIfAborted()
          if (signedInAccount() !== account) throw new Error('Your sign-in changed. Open search again.')
          const index = next++
          results[index] = await searchBoard(boards[index])
        }
      }))
      signal.throwIfAborted()
      if (signedInAccount() !== account) throw new Error('Your sign-in changed. Open search again.')
      return { items: results.flatMap(result => result.items), note: [...new Set(results.map(result => result.note).filter(Boolean))].join(' ') }
    },
  }
}
