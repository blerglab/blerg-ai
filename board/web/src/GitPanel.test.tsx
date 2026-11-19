import { render,screen,fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { vi,it,expect } from 'vitest';
import { GitPanel } from './GitPanel';
import { api } from './api';
vi.mock('./api',()=>({api:vi.fn()}));
it('shows local history without a provider URL',async()=>{
 vi.mocked(api).mockResolvedValue({branch:'work/card',commits:[{sha:'abcdef1234567890',message:'Local work',author:'Example',date:'2026-10-01T00:00:00Z'}]});
 render(<MemoryRouter><GitPanel cardId="c" boardId="b" cardNumber={1}/></MemoryRouter>);
 fireEvent.click(screen.getByRole('button',{name:/Git/}));
 expect(await screen.findByText('Local work')).toBeVisible();
 expect(screen.getByText('abcdef1')).toBeVisible();
 expect(screen.getByText(/Showing up to 30 recent commits/)).toBeVisible();
});
it('shows a workspace failure and offers retry',async()=>{
 vi.mocked(api).mockRejectedValue(new Error('Open a workspace for this card.'));
 render(<MemoryRouter><GitPanel cardId="c" boardId="b" cardNumber={1}/></MemoryRouter>);
 fireEvent.click(screen.getByRole('button',{name:/Git/}));
 expect(await screen.findByRole('alert')).toHaveTextContent('Open a workspace');
 expect(screen.getByRole('button',{name:'Retry'})).toBeVisible();
});
it('resets the workspace choice when switching cards',async()=>{
 vi.mocked(api).mockResolvedValue({branch:'work/one',commits:[]});
 const {rerender}=render(<MemoryRouter><GitPanel cardId="one" boardId="b" cardNumber={1}/></MemoryRouter>);
 fireEvent.click(screen.getByRole('button',{name:/Git/}));
 await screen.findByText('work/one');
 rerender(<MemoryRouter><GitPanel cardId="two" boardId="b" cardNumber={2}/></MemoryRouter>);
 expect(screen.getByRole('button',{name:/Git/})).toHaveAttribute('aria-expanded','false');
});
it('keeps published commits and file counts visible after the workspace ends',async()=>{
 vi.mocked(api).mockRejectedValue(new Error('Workspace has ended.'));
 const summary=[{repository:'example-org/app',session_id:'session',branch:'work/card',base:'main',commits:[{sha:'abcdef1234567890',message:'Retained work',author:'Example',date:'2026-11-01T00:00:00Z'}],files:[{path:'app.go',adds:3,dels:1},{path:'logo.png',adds:0,dels:0,binary:true}]}];
 render(<MemoryRouter><GitPanel cardId="c" boardId="b" cardNumber={1} summary={summary}/></MemoryRouter>);
 fireEvent.click(screen.getByRole('button',{name:/Git/}));
 expect(await screen.findByText('Retained work')).toBeVisible();
 expect(screen.getByText('app.go')).toBeVisible();
 expect(screen.getByText('+3 −1')).toBeVisible();
 expect(screen.getByText('Binary')).toBeVisible();
 expect(screen.getByRole('link',{name:'Open full diff'})).toHaveAttribute('href','/boards/b/cards/1/diff?session_id=session&branch=work%2Fcard&base=main');
 expect(await screen.findByRole('alert')).toHaveTextContent('Workspace has ended.');
});
it('shows an empty published commit list explicitly',async()=>{
 vi.mocked(api).mockRejectedValue(new Error('Offline'));
 render(<MemoryRouter><GitPanel cardId="c" boardId="b" cardNumber={1} summary={[{repository:'example-org/app',session_id:'session',branch:'work/card',base:'main',commits:[],files:[]}]}/></MemoryRouter>);
 fireEvent.click(screen.getByRole('button',{name:/Git/}));
 expect(await screen.findByText('No commits recorded.')).toBeVisible();
});
it('explains why an ended workspace cannot serve a saved diff',async()=>{
 vi.mocked(api).mockResolvedValue({commits_error:'The workspace desktop is offline.'});
 render(<MemoryRouter><GitPanel cardId="ended" boardId="b" cardNumber={1} summary={[{repository:'example-org/app',session_id:'session',branch:'work/card',base:'main',commits:[],files:[]}]}/></MemoryRouter>);
 fireEvent.click(screen.getByRole('button',{name:/Git/}));
 expect(await screen.findByRole('alert')).toHaveTextContent('The workspace desktop is offline.');
 expect(screen.getByText(/Ended workspaces cannot serve full diffs/)).toBeVisible();
});
