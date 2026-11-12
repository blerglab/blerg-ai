import {render} from '@testing-library/react'
import {expect, it, vi} from 'vitest'
import DashboardBlock from './DashboardBlock'
const state = vi.hoisted(() => ({rows: [] as Record<string, string | number>[], status: 'Source unavailable', observed: '2026-11-05T12:00:00Z'}))
vi.mock('./useDashboardDataset', () => ({useDashboardDataset: () => ({...state, ref: {current: null}})}))
vi.mock('./VisualBlock', () => ({default: () => null}))
const source = JSON.stringify({format:'blerg-dashboard',version:1,title:'Market · simulated',cards:[{id:'quote',kind:'stat',title:'Quote',span:1,data:{kind:'dataset',name:'blerg.mcp',parameters:{binding:'00000000-0000-4000-8000-000000000514'}},view:{value:'value',selection:'latest'}}]})
it.each(['closed','halted'])('keeps market %s independent of a provider outage and unknown entitlement', market_status => {
 state.rows=[{key:'quote',value:185.25,market_status}]
 const view=render(<DashboardBlock source={source} />)
 expect(view.container).toHaveTextContent(market_status==='closed'?'Market closed':'Trading halted')
 expect(view.container).toHaveTextContent('Entitlement unknown')
 expect(view.container).toHaveTextContent('Source unavailable')
 expect(view.container.querySelector('time')).toHaveAttribute('dateTime',state.observed)
 view.rerender(<DashboardBlock source={source} />)
})
it('does not infer market state from an unknown provider value or the calendar', () => {
 state.rows=[{key:'quote',value:185.25,market_status:'not-provider-evidence',entitlement:'unknown'}]
 const view=render(<DashboardBlock source={source} />)
 expect(view.container).toHaveTextContent('Market status unknown')
 expect(view.container).toHaveTextContent('Entitlement unknown')
 expect(view.container).not.toHaveTextContent('Market closed')
})
it('purges market facts together with revoked rows', () => {
 state.rows=[];state.status='Access removed'
 const view=render(<DashboardBlock source={source} />)
 expect(view.container).toHaveTextContent('Unknown')
 expect(view.container).not.toHaveTextContent('Market closed')
 expect(view.container).not.toHaveTextContent('Trading halted')
})
it('renders unsupported MCP cards as unavailable snapshots without refusing their native neighbors', () => {
 state.rows=[]
 const doc=JSON.parse(source)
 doc.cards.push({...doc.cards[0],id:'unsupported',title:'Unsupported trend',view:{value:'value',selection:'latest',sparkline:'value'}})
 const view=render(<DashboardBlock source={JSON.stringify(doc)} />)
 expect(view.container.querySelectorAll('.dashboard-card')).toHaveLength(2)
 expect(view.container).not.toHaveTextContent('Dashboard refused')
})
