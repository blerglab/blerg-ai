import type { CardTarget, ChatRefs } from "@blerglab/chat";
import { getAccessToken } from "./authClient";
import { api, type Board, type Card } from "./api";

// One discovery snapshot per chat/viewer; session remounts create new readers.
export function cardReaders(boardId?: string): Pick<ChatRefs, 'cards' | 'boardCards'> {
  let viewer = getAccessToken();
  let keys: Promise<Map<string, string>> | undefined;
  const boardCards = async (id: string, numbers: number[]) => {
    const current = getAccessToken();
    if (current !== viewer) { keys = undefined; viewer = current; }
    const found = new Map<number, CardTarget>();
    if (/^[A-Z][A-Z0-9]{1,4}$/.test(id)) {
      keys ??= api<Board[]>("/api/boards").then(boards => {
        const map = new Map<string, string>();
        for (const b of boards) for (const key of [b.key, ...(b.key_aliases ?? [])]) if (key) map.set(key, b.id);
        return map;
      }).catch(err => { keys = undefined; throw err; });
      id = (await keys).get(id) ?? '';
      if (!id) return found;
    }
    await Promise.all(numbers.map(async number => {
      try {
        const c = await api<Card>(`/api/boards/${encodeURIComponent(id)}/cards/by-number/${number}`);
        found.set(number, { kind: 'card', source: 'blerg-board', boardId: id,
          key: c.reference || `#${c.number}`, title: c.title });
      } catch { /* Missing or inaccessible cards stay text. */ }
    }));
    return found;
  };
  return { cards: boardId ? numbers => boardCards(boardId, numbers) : undefined, boardCards };
}
