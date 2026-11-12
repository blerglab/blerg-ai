import { render,screen } from '@testing-library/react';
import { MemoryRouter,Routes,Route } from 'react-router-dom';
import { vi,it,expect,afterEach } from 'vitest';
import { DiffPage } from './DiffPage';
vi.mock('./api',()=>({api:vi.fn(async()=>[{id:'card',number:1,title:'Local card'}])}));
vi.mock('./authClient',()=>({getAccessToken:()=> 'viewer-proof'}));
afterEach(()=>vi.unstubAllGlobals());
function show() {return render(<MemoryRouter initialEntries={['/boards/b/cards/1/diff?member_id=one&generation=2&launch_generation=launch']}><Routes><Route path="/boards/:id/cards/:number/diff" element={<DiffPage/>}/></Routes></MemoryRouter>);}
it('carries the selected repository and signed-in viewer to local diff reads',async()=>{
 const fetcher=vi.fn(async()=>({ok:true,text:async()=>'+local change'}));vi.stubGlobal('fetch',fetcher);
 show();
 expect(await screen.findByText('+local change')).toBeVisible();
 expect(fetcher).toHaveBeenCalledWith('/api/cards/card/diff?member_id=one&generation=2&launch_generation=launch',expect.objectContaining({headers:{Authorization:'Bearer viewer-proof'}}));
});
it('finishes loading an empty local comparison',async()=>{
 vi.stubGlobal('fetch',vi.fn(async()=>({ok:true,text:async()=>''})));
 show();
 expect(await screen.findByText('No changes beyond the base branch.')).toBeVisible();
 expect(screen.queryByText('fetching diff…')).toBeNull();
});
