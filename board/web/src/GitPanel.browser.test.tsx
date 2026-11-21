import { it, expect, vi, afterEach } from 'vitest';
import { render,screen,cleanup } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { page,userEvent } from 'vitest/browser';
import { GitPanel } from './GitPanel';
import './styles.css';
vi.mock('./api',()=>({api:vi.fn(async()=>({branch:'work/'+ 'long-branch'.repeat(16),commits:[{sha:'abcdef1234567890',message:'Local change in a private repository',author:'Example'.repeat(40),date:'2026-10-01T00:00:00Z'}]}))}));
afterEach(cleanup);
it.each([390,3840])('shows local commits and diff at %i px',async width=>{
 await page.viewport(width,width===390?844:2160);
 render(<MemoryRouter><div style={{width:Math.min(width-24,800),margin:12}}><GitPanel cardId="c" boardId="b" cardNumber={1}/></div></MemoryRouter>);
 await userEvent.click(screen.getByRole('button',{name:/Git/}));
 expect(await screen.findByText('Local change in a private repository')).toBeVisible();
 expect(screen.getByRole('link',{name:'View in blerg-board'})).toHaveAttribute('href','/boards/b/cards/1/diff');
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
});
