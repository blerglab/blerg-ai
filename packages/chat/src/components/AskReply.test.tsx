import {fireEvent, render, screen, waitFor} from '@testing-library/react'
import {expect, it, vi} from 'vitest'
import AskReply from './AskReply'
for (const [label, value] of [['Approve','yes'],['Decline','no']]) it(`sends exact ${value} for ${label}`, async () => {
 const answer=vi.fn(async()=>true)
 render(<AskReply binary answer={answer} messageId="decision" />)
 expect(screen.queryByRole('textbox')).toBeNull()
 fireEvent.click(screen.getByRole('button',{name:label}))
 await waitFor(()=>expect(answer).toHaveBeenCalledWith(value,'decision'))
})
it('reveals a conversational reply and retains it after a failed send', async()=>{
 render(<AskReply binary answer={async()=>false} />)
 fireEvent.click(screen.getByRole('button',{name:'Ask Main'}))
 fireEvent.change(screen.getByRole('textbox'),{target:{value:'Which board?'}})
 fireEvent.click(screen.getByRole('button',{name:'Answer'}))
 await screen.findByRole('status')
 expect(screen.getByRole('textbox')).toHaveValue('Which board?')
})
it('keeps ordinary questions as text replies',()=>{
 render(<AskReply answer={async()=>true} />)
 expect(screen.getByRole('textbox')).toBeTruthy()
 expect(screen.queryByRole('button',{name:'Approve'})).toBeNull()
})
it('keeps a conversational reply distinct from an explicit answer',()=>{
 render(<AskReply binary answer={async()=>true} reply={async()=>true} />)
 fireEvent.click(screen.getByRole('button',{name:'Ask Main'}))
 expect(screen.getByRole('button',{name:'Ask Main'})).toBeTruthy()
})

it('keeps an opt-in compact composer unfocused until the person chooses it', () => {
 render(<AskReply compact answer={async()=>true} />)
 expect(screen.getByRole('textbox')).not.toHaveFocus()
 expect(screen.getByRole('textbox').closest('form')).toHaveClass('ask-reply-compact')
})
