import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { createElement } from 'react';
import { Markdown, createRefStore, splitRefs } from '@blerglab/chat';
import { RefContext } from '../../../packages/chat/src/components/RefLinks';
import { cardReaders } from './cardRefs';
import { api } from './api';
vi.mock('./api', () => ({ api: vi.fn() }));
beforeEach(() => vi.mocked(api).mockReset());

describe('Board key references', () => {
  it.each(['BLG', 'OLD', 'board-a'])('resolves %s without listing cards', async key => {
    vi.mocked(api).mockImplementation(async path => path === '/api/boards'
      ? [{ id: 'board-a', key: 'BLG', key_aliases: ['OLD'] }]
      : { board_id: 'board-a', number: 205, reference: 'BLG-205', title: 'Archived work', archived_at: '2026-01-01' });
    const cards = await cardReaders().boardCards!(key, [205]);
    expect(cards.get(205)).toMatchObject({ boardId: 'board-a', key: 'BLG-205', title: 'Archived work' });
    expect(api).toHaveBeenCalledWith('/api/boards/board-a/cards/by-number/205');
    expect(vi.mocked(api).mock.calls.some(([path]) => path.endsWith('/cards'))).toBe(false);
  });
  it('keeps ordinary prose plain with one discovery and no card reads', async () => {
    vi.mocked(api).mockResolvedValue([{ id: 'board-a', key: 'BLG' }]);
    const text = 'UTF-8 ISO-8601 SHA-256 AES-128 COVID-19 GPT-4 555-1234 2026-10';
    const refs = cardReaders();
    const store = createRefStore();
    store.setResolvers(refs);
    render(createElement(RefContext.Provider, { value: { store, sessionId: 's', refs } }, createElement(Markdown, { text })));
    await waitFor(() => expect(api).toHaveBeenCalledTimes(1));
    for (const p of splitRefs(text)) if ('key' in p) await waitFor(() => expect(store.get(p.key).state).toBe('none'));
    expect(screen.queryByTestId('md-ref')).toBeNull();
    expect(document.querySelector('.md')?.textContent).toBe(text);
    expect(api).toHaveBeenCalledTimes(1);
  });
});
