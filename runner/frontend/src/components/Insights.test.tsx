import { beforeEach, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { InsightsDashboard } from './Insights'
import { richInsights, richPrices } from '../test/insightsFixture'
import { richActivity } from '../test/activityFixture'
import { clearInsightsCache } from '../lib/insightsCache'

const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
beforeEach(()=>{clearInsightsCache();localStorage.clear();apiFetch.mockReset();apiFetch.mockImplementation(async(url:string)=>({ok:true,json:async()=>url.includes('/activity')?richActivity:url.includes('/prices')?richPrices:richInsights}))})
it('fetches summary and activity together and keeps the owner and workspace scope',async()=>{
 render(<MemoryRouter><InsightsDashboard workspaceId="workspace-a"/></MemoryRouter>)
 expect(apiFetch).toHaveBeenCalledWith('/api/insights?range=7d&scope=mine&workspace=workspace-a')
 expect(apiFetch).toHaveBeenCalledWith('/api/insights/activity?range=7d&workspace=workspace-a',expect.anything())
 await screen.findByTestId('question-health')
 expect(screen.queryByText('Details',{selector:'summary'})).toBeNull()
 expect(screen.queryByRole('button',{name:'Everyone'})).toBeNull()
 fireEvent.click(screen.getByRole('button',{name:'Time'}))
 await screen.findByTestId('question-time')
 expect(apiFetch.mock.calls.filter(([url])=>url.includes('/activity'))).toHaveLength(1)
})
it('refetches both reports when the range changes',async()=>{
 render(<MemoryRouter><InsightsDashboard/></MemoryRouter>)
 await screen.findByTestId('question-health')
 fireEvent.click(screen.getByRole('button',{name:'30d'}))
 await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/insights?range=30d&scope=mine'))
 expect(apiFetch).toHaveBeenCalledWith('/api/insights/activity?range=30d',expect.anything())
})
it('paints a cached range immediately when returning from an unanswered range',async()=>{
 render(<MemoryRouter><InsightsDashboard/></MemoryRouter>)
 await screen.findByTestId('question-health')
 apiFetch.mockImplementation(()=>new Promise(()=>{}))
 fireEvent.click(screen.getByRole('button',{name:'30d'}))
 expect(screen.queryByTestId('question-health')).toBeNull()
 fireEvent.click(screen.getByRole('button',{name:'7d'}))
 expect(screen.getByTestId('question-health')).toBeInTheDocument()
 expect(screen.queryByText('Loading insights…')).toBeNull()
})
it('shows a retryable error rather than empty data for a failed summary',async()=>{
 apiFetch.mockImplementation(async(url:string)=>({ok:url.includes('/activity'),status:500,json:async()=>richActivity}))
 render(<MemoryRouter><InsightsDashboard/></MemoryRouter>)
 expect(await screen.findByRole('alert')).toHaveTextContent('Could not load insights')
 expect(screen.getByRole('button',{name:'Try again'})).toBeInTheDocument()
})
it('puts the price editor only in the spending view and loads it on demand',async()=>{
 render(<MemoryRouter><InsightsDashboard/></MemoryRouter>)
 await screen.findByTestId('question-health')
 expect(apiFetch.mock.calls.some(([url])=>url.includes('/prices'))).toBe(false)
 fireEvent.click(screen.getByRole('button',{name:'Spending'}))
 fireEvent.click(await screen.findByText('Model prices',{selector:'summary'}))
 await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/insights/prices'))
 expect(await screen.findByTestId('price-editor')).toBeInTheDocument()
})
it('validates model prices before saving and accepts an explicit zero',async()=>{
 render(<MemoryRouter><InsightsDashboard/></MemoryRouter>)
 await screen.findByTestId('question-health')
 fireEvent.click(screen.getByRole('button',{name:'Spending'}))
 fireEvent.click(await screen.findByText('Model prices',{selector:'summary'}))
 await screen.findByTestId('price-editor')
 const model=richInsights.tokens.unpriced_models[0]
 const input=screen.getByLabelText(`Input price per million tokens, ${model}`)
 fireEvent.change(input,{target:{value:'-1'}})
 fireEvent.change(screen.getByLabelText(`Output price per million tokens, ${model}`),{target:{value:'2'}})
 fireEvent.click(screen.getByRole('button',{name:`Save ${model}`}))
 expect(await screen.findByRole('alert')).toHaveTextContent('zero or more')
 expect(apiFetch.mock.calls.some(([,init])=>init?.method==='PUT')).toBe(false)
 fireEvent.change(input,{target:{value:'0'}})
 fireEvent.click(screen.getByRole('button',{name:`Save ${model}`}))
 await waitFor(()=>expect(apiFetch).toHaveBeenCalledWith('/api/insights/prices',expect.objectContaining({method:'PUT',body:JSON.stringify({model,input_per_mtok:0,output_per_mtok:2,cache_read_per_mtok:0,cache_write_per_mtok:0})})))
})
it('keeps the price editor unavailable to a member',async()=>{
 apiFetch.mockImplementation(async(url:string)=>({ok:true,json:async()=>url.includes('/activity')?richActivity:{...richInsights,is_admin:false}}))
 render(<MemoryRouter><InsightsDashboard/></MemoryRouter>)
 await screen.findByTestId('question-health')
 fireEvent.click(screen.getByRole('button',{name:'Spending'}))
 expect(screen.queryByText('Model prices',{selector:'summary'})).toBeNull()
 expect(apiFetch.mock.calls.some(([url])=>url.includes('/prices'))).toBe(false)
})
