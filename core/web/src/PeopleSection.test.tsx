import { expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import PeopleSection from "./PeopleSection";
const mock = vi.hoisted(() => ({ fetch: vi.fn() }));
vi.mock("./apiFetch", () => ({ apiFetch: mock.fetch }));
it("invites a member, reveals a link once, and confirms disabling", async () => {
    const calls: string[] = [];
    mock.fetch.mockImplementation(async (path: string, init?: RequestInit) => {
        calls.push(`${init?.method ?? 'GET'} ${path}`);
        return { ok: true, json: async () => path === '/api/people' && init?.method === 'POST' ? { code: 'a'.repeat(43) } : { people: [{ id: 'p', name: 'New person', email: 'person@example.com', username: 'person', role: 'member', pending: true, disabled: false }] } };
    });
    render(<PeopleSection />);
    await screen.findByText('New person');
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'Someone' } });
    fireEvent.change(screen.getByLabelText('Email'), { target: { value: 'someone@example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add a person' }));
    expect(await screen.findByLabelText('Invite link')).toHaveValue(`${location.origin}/invite#code=${'a'.repeat(43)}`);
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(screen.queryByLabelText('Invite link')).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Disable' }));
    expect(calls).not.toContain('POST /api/people/p/disable');
    fireEvent.click(screen.getByRole('button', { name: 'Disable person' }));
    await waitFor(() => expect(calls).toContain('POST /api/people/p/disable'));
});
it('lets an administrator finish desktop disconnection after a partial disable',async()=>{
 let disabled=false;let attempts=0;
 mock.fetch.mockImplementation(async (_path:string,init?:RequestInit)=>{
  if(init?.method==='POST'){disabled=true;attempts++;return {ok:attempts>1,status:attempts>1?200:503};}
  return {ok:true,json:async()=>({people:[{id:'p',name:'Person',email:'person@example.com',username:'person',role:'member',pending:false,disabled}]})};
 });
 render(<PeopleSection/>);await screen.findByText('Person');
 fireEvent.click(screen.getByRole('button',{name:'Disable'}));fireEvent.click(screen.getByRole('button',{name:'Disable person'}));
 fireEvent.click(await screen.findByRole('button',{name:'Disconnect desktops'}));
 await waitFor(()=>expect(attempts).toBe(2));
});

it('explains a busy retry without claiming the person is disabling themselves', async () => {
 mock.fetch.mockImplementation(async (_path: string, init?: RequestInit) => init?.method === 'POST'
  ? { ok: false, status: 503 }
  : { ok: true, json: async () => ({ people: [{ id: 'p', name: 'Person', email: '', username: 'person', role: 'member', pending: false, disabled: false }] }) })
 render(<PeopleSection />)
 await screen.findByText('Person')
 fireEvent.click(screen.getByRole('button', { name: 'Disable' }))
 fireEvent.click(screen.getByRole('button', { name: 'Disable person' }))
 expect(await screen.findByRole('alert')).toHaveTextContent('Try again')
 expect(screen.getByRole('alert')).not.toHaveTextContent('cannot disable yourself')
})
