import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { page } from 'vitest/browser';
import SessionList from './SessionList';
import { useSessionStore } from '../hooks/useSessionStore';
import '../index.css';
vi.mock('../ws', () => import('../test/wsMock'));
afterEach(cleanup);
for (const width of [390,3840]) it(`shows release and upgrade links without clipping at ${width}px`,async()=>{
  await page.viewport(width,1000);
  useSessionStore.setState({daemons:[],sessions:[],serverRelease:'0.5.0',serverCommit:'fixture',latestVersion:'0.5.1',latestUrl:'https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1'});
  render(<MemoryRouter><div style={{width:width < 640 ? "100%" : 280}}><SessionList variant={width < 640 ? 'page' : 'sidebar'} /></div></MemoryRouter>);
  const notice = await screen.findByRole('link',{name:/v0.5.1 available/});
  const upgrade = screen.getByRole('link',{name:'How to upgrade'});
  expect(upgrade.getAttribute('href')).toContain('/upgrade');
  for (const el of [notice,upgrade]) {
    const box = el.getBoundingClientRect();
    expect(box.width).toBeGreaterThan(0);
    expect(box.left).toBeGreaterThanOrEqual(0);
    expect(box.right).toBeLessThanOrEqual(width);
  }
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
});
