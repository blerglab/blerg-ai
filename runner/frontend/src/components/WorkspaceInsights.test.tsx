import { it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { InsightsDashboard } from './Insights'
import { richInsights } from '../test/insightsFixture'
import { richActivity } from '../test/activityFixture'
const apiFetch=vi.hoisted(()=>vi.fn())
vi.mock('../apiFetch',()=>({apiFetch}))
it('keeps all question views in one workspace without repeating the tab name',async()=>{
 localStorage.clear()
 apiFetch.mockImplementation(async(url:string)=>({ok:true,json:async()=>url.startsWith('/api/insights/activity')?richActivity:{...richInsights,range:{...richInsights.range,label:'7d'},workspace_name:'My workspace'}}))
 render(<MemoryRouter><InsightsDashboard workspaceId="root-a"/></MemoryRouter>)
 await screen.findByTestId('question-health')
 expect(screen.queryByText('My workspace')).toBeNull()
 expect(screen.queryByRole('button',{name:'Everyone'})).toBeNull()
 expect(apiFetch).toHaveBeenCalledWith('/api/insights?range=7d&scope=mine&workspace=root-a')
 for(const section of ['Card cost','Time','Spending']){
 fireEvent.click(screen.getByRole('button',{name:section}))
 await screen.findByTestId('activity-report')
 expect(apiFetch).toHaveBeenCalledWith('/api/insights/activity?range=7d&workspace=root-a',expect.anything())
 }
 expect(screen.queryByText('My workspace')).toBeNull()
})
