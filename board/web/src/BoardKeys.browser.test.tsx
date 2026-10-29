import { afterEach, describe, expect, it, vi } from 'vitest';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { page, userEvent } from 'vitest/browser';
import { BoardKeySettings } from './BoardKeySettings';
import { Boards } from './App';
import { MeProvider, useMe } from './me';
import { api, type Board } from './api';
import './styles.css';

vi.mock('./api', async original => ({
  ...await original<typeof import('./api')>(),
  api: vi.fn(async (path: string) => path.startsWith('/api/board-key-suggestion?') ? { key: 'BLG2' } : path === '/api/me' ? { is_admin: true, is_human: true } : path === '/api/repo-choices' ? { repos: [] } : []),
}));
afterEach(() => { cleanup(); vi.mocked(api).mockClear(); });

function ViewerReady() { return useMe() ? <span>Viewer ready</span> : null; }

describe('Board keys', () => {
  it.each([390, 3840])('suggests and submits the chosen key on creation at %i px', async width => {
    await page.viewport(width, width === 390 ? 844 : 2160);
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    await userEvent.click(await screen.findByText('New board'));
    await userEvent.fill(screen.getByPlaceholderText('board name'), 'Blerg');
    const input = screen.getByLabelText('Board key');
    // The suggestion is debounced, and this is the file's first render in a cold browser on CI.
    await waitFor(() => expect(input).toHaveValue('BLG2'), { timeout: 5000 });
    await userEvent.fill(input, 'BLGR');
    await userEvent.click(screen.getByText('Create board'));
    expect(vi.mocked(api).mock.calls.some(([path, init]) => path === '/api/boards' && (init?.json as { key?: string })?.key === 'BLGR')).toBe(true);
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
  });

  it.each([390, 3840])('requires confirmation of an upgrade suggestion and reports collisions at %i px', async width => {
    await page.viewport(width, width === 390 ? 844 : 2160);
    const board = { id: 'b1', name: 'AfterDark', key: '', suggested_key: 'AD' } as Board;
    const changed = vi.fn();
    render(<MeProvider><BoardKeySettings board={board} onChanged={changed} /></MeProvider>);
    const input = await screen.findByLabelText('Key (2–5 uppercase letters or digits, starting with a letter)');
    expect(input).toHaveValue('AD');
    expect(vi.mocked(api).mock.calls.filter(([, init]) => init?.method === 'PATCH')).toHaveLength(0);
    vi.mocked(api).mockRejectedValueOnce(new Error('key: is already used by another board or one of its aliases'));
    await userEvent.click(screen.getByText('Confirm key'));
    expect(await screen.findByRole('alert')).toHaveTextContent('already used');
    await userEvent.fill(input, 'AD2');
    await userEvent.click(screen.getByText('Confirm key'));
    await waitFor(() => expect(changed).toHaveBeenCalledOnce());
    expect(input.getBoundingClientRect().right).toBeLessThanOrEqual(width);
  });
});

it.each([
  [{ is_admin: true, board_id: 'b1' }, true],
  [{ is_admin: true, board_id: 'other' }, false],
  [{ is_admin: false }, false],
] as const)('gates board key actions on board scope: %j', async (me, allowed) => {
  vi.mocked(api).mockImplementation(async path => path === '/api/me' ? me : []);
  const board = { id: 'b1', name: 'Work', key: '', suggested_key: 'WK' } as Board;
  render(<MeProvider><ViewerReady /><BoardKeySettings board={board} /></MeProvider>);
  await waitFor(() => expect(vi.mocked(api)).toHaveBeenCalledWith('/api/me'));
  await screen.findByText('Viewer ready');
  if (allowed) expect(await screen.findByText('Confirm key')).toBeVisible();
  else { expect(await screen.findByText('A board admin can confirm this key.')).toBeVisible(); expect(screen.queryByText('Confirm key')).toBeNull(); }
});
