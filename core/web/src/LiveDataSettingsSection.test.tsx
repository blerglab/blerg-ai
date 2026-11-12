import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, expect, it, vi } from 'vitest';
import LiveDataSettingsSection from './LiveDataSettingsSection';
import { apiFetch } from './apiFetch';
vi.mock('./apiFetch', () => ({ apiFetch: vi.fn() }));
afterEach(() => vi.restoreAllMocks());
it('defaults off and sends explicit acknowledgement without starting polling', async () => {
 const view={unsafe_host_opt_in:false,polling_enabled:false,bindings:[]};
 vi.mocked(apiFetch).mockImplementation(async (_url,options) => {
  if(options?.method==='PUT') view.unsafe_host_opt_in=true;
  return new Response(JSON.stringify(view),{status:200});
 });
 render(<LiveDataSettingsSection />);
 const checkbox=await screen.findByRole('checkbox');expect(checkbox).not.toBeChecked();
 await act(async()=>{fireEvent.click(checkbox);});
 expect(apiFetch).toHaveBeenCalledWith('/api/settings/live-data',expect.objectContaining({body:JSON.stringify({unsafe_host_opt_in:true,acknowledgement:'Agents on this machine can reach the polling credentials.'})}));
 expect(await screen.findByRole('status')).toHaveTextContent('Saved.');expect(checkbox).toBeChecked();
});
it('keeps confirmed state on a failed save and logs a static cause', async () => {
 const warn=vi.spyOn(console,'warn').mockImplementation(()=>{});
 vi.mocked(apiFetch).mockImplementation(async (_url,options)=>new Response(JSON.stringify({unsafe_host_opt_in:true,bindings:[]}),{status:options?.method==='PUT'?503:200}));
 render(<LiveDataSettingsSection />);
 const checkbox=await screen.findByRole('checkbox');fireEvent.click(checkbox);
 await screen.findByRole('alert');expect(checkbox).toBeChecked();expect(warn).toHaveBeenCalledWith('live_data_settings:save_failed');
});
it('shows safe load failure without exposing response text', async () => {
 vi.spyOn(console,'warn').mockImplementation(()=>{});
 vi.mocked(apiFetch).mockResolvedValue(new Response('secret-canary',{status:500}));
 render(<LiveDataSettingsSection />);
 await waitFor(()=>expect(screen.getByRole('alert')).toHaveTextContent('Could not load live data settings.'));
 expect(screen.queryByText('secret-canary')).not.toBeInTheDocument();
});
it('shows the exact tool hash and definition in Settings', async () => {
 vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({unsafe_host_opt_in:false,bindings:[{binding_id:'binding',revision:1,digest:'digest',connection_name:'Market',tool:'quotes',mode:'off',status:'pending',hash:'pin-canary',tool_definition:{name:'quotes',inputSchema:{type:'object'}}}]})));
 render(<LiveDataSettingsSection />);
 expect(await screen.findByText('pin-canary')).toBeInTheDocument();
 expect(screen.getByText('Tool definition')).toBeInTheDocument();
 expect(screen.getByText(/"name": "quotes"/)).toBeInTheDocument();
});
