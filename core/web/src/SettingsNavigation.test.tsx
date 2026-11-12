import { afterEach, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import Settings from './Settings';
import * as auth from './authClient';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); history.replaceState(null, '', '/settings'); });
function setup(section = '', admin = false) {
  history.replaceState(null, '', `/settings${section ? `?section=${section}` : ''}`);
  vi.spyOn(auth, 'getAccessToken').mockReturnValue(`x.${btoa(JSON.stringify({caps: admin ? ['account.manage'] : ['card.read']}))}.y`);
  vi.stubGlobal('fetch', vi.fn(async () => ({ok:true, status:200, json:async () => []})));
  render(<Settings />);
}
it('defaults to Account and navigates sections with deep links and Back', () => {
  setup();
  expect(screen.getByRole('navigation', {name:'Settings sections'})).toBeTruthy();
  expect(screen.getByRole('heading', {name:'Account'})).toBeTruthy();
  expect(screen.queryByRole('heading', {name:'Credentials'})).toBeNull();
  fireEvent.click(screen.getByRole('link', {name:/Integrations/}));
  expect(location.search).toBe('?section=integrations');
  expect(screen.getByRole('heading', {name:'Credentials'})).toBeTruthy();
  history.replaceState(null, '', '/settings?section=account');
  fireEvent.popState(window);
  expect(screen.getByRole('heading', {name:'Account'})).toBeTruthy();
});
it('opens an admin section directly and hides it from other accounts', () => {
  setup('install', true);
  expect(screen.getByRole('link', {name:/Install/}).getAttribute('aria-current')).toBe('page');
  expect(screen.getByRole('heading', {name:'Owner shells'})).toBeTruthy();
});
it.each(['install', 'missing'])('falls back to Account for unauthorized or unknown deep link %s', section => {
  setup(section);
  expect(screen.queryByRole('link', {name:/Install/})).toBeNull();
  expect(screen.queryByRole('heading', {name:'Owner shells'})).toBeNull();
  expect(screen.getByRole('heading', {name:'Account'})).toBeTruthy();
});
