import { render, screen, cleanup } from '@testing-library/react'
import { useRef } from 'react'
import { afterEach, expect, it } from 'vitest'
import { useChangePulses } from './changePulse'
function Rows({ values, ready = true }: { values: Map<string,string>; ready?: boolean }) {
 const root=useRef<HTMLUListElement>(null)
 useChangePulses(root,values,'data-change-key',ready)
 return <ul ref={root}>{[...values.keys()].map(key=><li key={key} data-testid={key} data-change-key={key}>{key}</li>)}</ul>
}
afterEach(cleanup)
it('highlights additions and transitions without flashing the initial or unchanged snapshot',()=>{
 const view=render(<Rows values={new Map([['one','false']])}/>)
 expect(screen.getByTestId('one')).not.toHaveClass('change-pulse')
 view.rerender(<Rows values={new Map([['one','true'],['two','false']])}/>)
 expect(screen.getByTestId('one')).toHaveClass('change-pulse')
 expect(screen.getByTestId('two')).toHaveClass('change-pulse')
 for(const node of screen.getAllByRole('listitem')) node.dispatchEvent(new Event('animationend'))
 view.rerender(<Rows values={new Map([['one','true'],['two','false']])}/>)
 expect(screen.getByTestId('one')).not.toHaveClass('change-pulse')
 expect(screen.getByTestId('two')).not.toHaveClass('change-pulse')
})

it('waits for the initial asynchronous snapshot before highlighting updates',()=>{
 const view=render(<Rows values={new Map()} ready={false}/>)
 view.rerender(<Rows values={new Map([['one','false']])} ready/>)
 expect(screen.getByTestId('one')).not.toHaveClass('change-pulse')
 view.rerender(<Rows values={new Map([['one','true']])} ready/>)
 expect(screen.getByTestId('one')).toHaveClass('change-pulse')
})
