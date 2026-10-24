import { afterEach, expect, it, vi } from 'vitest';
import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { page } from 'vitest/browser';
import { Shell } from './App';
import './styles.css';
vi.mock('./api', async original => ({
  ...await original<typeof import('./api')>(),
  api: vi.fn(async (path: string) => {
    if (path === '/api/version') return {release:'0.5.0',commit:'fixture',latest_version:'0.5.1',latest_url:'https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1'};
    if (path === '/api/me') return {is_admin:true,is_human:true};
    return [];
  }),
}));
afterEach(cleanup);
for (const width of [390,3840]) it(`shows release and upgrade links without clipping at ${width}px`,async()=>{
  await page.viewport(width,1000);
  render(<MemoryRouter><Shell><div /></Shell></MemoryRouter>);
  const notice = await screen.findByRole('link',{name:/v0.5.1 available/});
  const upgrade = screen.getByRole('link',{name:'How to upgrade'});
  expect(upgrade.getAttribute('href')).toContain('/upgrade');
  expect(notice.getAttribute('href')).toBe('https://github.com/blerglab/blerg-ai/releases/tag/v0.5.1');
  for (const el of [notice,upgrade]) {
    const box = el.getBoundingClientRect();
    expect(box.width).toBeGreaterThan(0);
    expect(box.left).toBeGreaterThanOrEqual(0);
    expect(box.right).toBeLessThanOrEqual(width);
  }
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
});
