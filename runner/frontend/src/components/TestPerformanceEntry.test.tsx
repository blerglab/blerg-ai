import {it,expect,vi} from 'vitest'
import {render,screen,fireEvent} from '@testing-library/react'
import {MemoryRouter} from 'react-router-dom'
import {InsightsDashboard} from './Insights'
vi.mock('../apiFetch',()=>({apiFetch:vi.fn(()=>new Promise(()=>{}))}))
it('opens Test Performance as a separate tab from Insights',()=>{
 const receive=vi.fn();window.addEventListener('blerg-open-test-performance',receive)
 render(<MemoryRouter><InsightsDashboard workspaceId="example-workspace"/></MemoryRouter>)
 fireEvent.click(screen.getByRole('button',{name:'Test performance'}))
 expect(receive).toHaveBeenCalledOnce()
 expect((receive.mock.calls[0][0] as CustomEvent).detail.workspace).toBe('example-workspace')
 window.removeEventListener('blerg-open-test-performance',receive)
})
