import { expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import TimeChart from './TimeChart'

it('makes bar values readable through keyboard focus and tap',()=>{
 render(<TimeChart buckets={[{at:'2026-11-01T00:00:00Z',values:{test:3}}]} series={[{key:'test',label:'Tests'}]} label="Failures"/>)
 const bar=screen.getByRole('button',{name:'Nov 1 · Tests: 3'})
 expect(bar).toHaveAttribute('tabindex','0')
 fireEvent.focus(bar)
 expect(screen.getByRole('status')).toHaveTextContent('Nov 1 · Tests: 3')
 fireEvent.click(bar)
 expect(screen.getByRole('status')).toHaveTextContent('Nov 1 · Tests: 3')
 fireEvent.keyDown(bar,{key:'Enter'})
 expect(screen.getByRole('status')).toHaveTextContent('Tests: 3')
})

it('keeps legend visibility separate from opening a breakdown',()=>{
 const select=vi.fn()
 render(<TimeChart buckets={[{at:'2026-11-01T00:00:00Z',values:{gates:3,building:5}}]} series={[{key:'gates',label:'Gates'},{key:'building',label:'Building'}]} label="Time" onSelectCategory={select}/>)
 fireEvent.click(screen.getByRole('button',{name:'Gates'}))
 expect(screen.getByRole('button',{name:'Gates'})).toHaveAttribute('aria-pressed','false')
 expect(screen.queryByRole('button',{name:'Nov 1 · Gates: 3'})).toBeNull()
 expect(select).not.toHaveBeenCalled()
 fireEvent.click(screen.getByRole('button',{name:'Show Gates breakdown'}))
 expect(select).toHaveBeenLastCalledWith('gates')
 fireEvent.click(screen.getByRole('button',{name:'Gates'}))
 expect(screen.getByRole('button',{name:'Nov 1 · Gates: 3'})).toBeInTheDocument()
 fireEvent.click(screen.getByRole('button',{name:'Nov 1 · Building: 5'}))
 expect(select).toHaveBeenLastCalledWith('building')
})

it('summarizes a busy day on hover and lists it only when that day is chosen, capped',()=>{
 const events=Array.from({length:40},(_,i)=>({at:'2026-10-30T12:00:00Z',label:`Event ${i+1}`}))
 const {container}=render(<TimeChart buckets={[{at:'2026-10-30T00:00:00Z',values:{test:1}},{at:'2026-10-31T00:00:00Z',values:{test:2}}]} series={[{key:'test',label:'Tests'}]} events={events} label="Time"/>)
 const title=container.querySelector('.time-chart-event title')!.textContent!
 expect(title).toBe('Oct 30: 40 events · Event 1 · Event 2 · Event 3 · and 37 more')
 expect(screen.queryByText('Event 12')).toBeNull()
 const day=screen.getByRole('button',{name:'Oct 30: 40 events'})
 expect(day).toHaveAttribute('tabindex','0')
 fireEvent.click(day)
 const list=screen.getByRole('list',{name:'Oct 30 events'})
 expect(list.querySelectorAll('li')).toHaveLength(10)
 expect(screen.getByText('Showing 10 of 40')).toBeTruthy()
 fireEvent.keyDown(day,{key:'Enter'})
 expect(screen.queryByRole('list',{name:'Oct 30 events'})).toBeNull()
})
