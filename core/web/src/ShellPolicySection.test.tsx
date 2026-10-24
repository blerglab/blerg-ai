import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, expect, it, vi } from 'vitest';
import ShellPolicySection from './ShellPolicySection';
const mock = vi.hoisted(() => ({ fetch: vi.fn() }));
vi.mock('./apiFetch', () => ({ apiFetch: mock.fetch }));
beforeEach(() => mock.fetch.mockReset());
it('shows authoritative policy, disables while saving, and sends only the switch', async () => {
  mock.fetch.mockResolvedValueOnce({ ok: true, json: async () => ({ version: 1, revision: 1, enabled: true }) });
  mock.fetch.mockResolvedValueOnce({ ok: true, json: async () => ({ version: 1, revision: 2, enabled: false }) });
  render(<ShellPolicySection />);
  await waitFor(() => expect(screen.getByRole('switch')).toBeEnabled());
  expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'true');
  fireEvent.click(screen.getByRole('switch'));
  expect(screen.getByRole('switch')).toBeDisabled();
  await waitFor(() => expect(screen.getByRole('switch')).toHaveAttribute('aria-checked', 'false'));
  expect(mock.fetch.mock.calls[1][1].body).toBe('{"enabled":false}');
});
it('does not pretend policy is enabled when the authority is unavailable', async () => {
  mock.fetch.mockRejectedValueOnce(new Error('offline'));
  render(<ShellPolicySection />);
  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('unavailable'));
  expect(screen.getByRole('switch')).toBeDisabled();
});
