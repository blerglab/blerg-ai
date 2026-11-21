import { cleanup, render, screen, fireEvent, waitFor } from '@testing-library/react'
import { afterEach, expect, it, vi } from 'vitest'
import { page } from 'vitest/browser'
import CronForm from './CronForm'
import { renderEvent } from '@blerglab/chat'
const requests = vi.hoisted(() => ({ saves: [] as Record<string,unknown>[] }))
vi.mock('../apiFetch', () => ({apiFetch: async (url: string, init?: RequestInit) => {
 const reply = (value: unknown) => ({ok:true,status:200,json:async()=>value})
 if (init?.method==='POST') {requests.saves.push(JSON.parse(String(init.body)));return reply({id:'cron'})}
 if (url.startsWith('/api/runner/launch-options')) return reply({runtime:'docker',state:'known',engines:[{id:'claude',name:'Claude',restricted:true}]})
 if (url==='/api/session-boards') return reply({configured:true,boards:[]})
 if (url==='/api/daemons') return reply([])
 if (url==='/api/cluster/status') return reply({configured:false})
 if (url==='/api/mcp/connections') return reply({connections:[]})
 return reply({models:[],source:'none'})
}}))
afterEach(() => {cleanup();requests.saves=[]})
for (const width of [390,3840]) it(`requires exact command approval and shows bounded results at ${width}`,async () => {
 await page.viewport(width,900)
 render(<CronForm onSaved={()=>{}} onCancel={()=>{}}/>)
 await waitFor(() => expect(screen.getByRole('button',{name:'Create cron'})).not.toBeDisabled())
 fireEvent.change(screen.getByLabelText('Name'),{target:{value:'Health'}})
 fireEvent.change(screen.getByLabelText('Prompt'),{target:{value:'Report health'}})
 const commands=screen.getByLabelText('Fixed command steps (JSON)')
 fireEvent.change(commands,{target:{value:'[{"argv":["echo","health"],"timeout_seconds":10,"max_output_bytes":1024}]'}})
 fireEvent.click(screen.getByRole('button',{name:'Create cron'}))
 expect(await screen.findByText('Approve the exact fixed commands before saving.')).toBeTruthy()
 expect(requests.saves).toHaveLength(0)
 fireEvent.click(screen.getByLabelText('I approve these exact commands for every run.'))
 fireEvent.click(screen.getByRole('button',{name:'Create cron'}))
 await waitFor(()=>expect(requests.saves).toHaveLength(1))
 expect(commands.getBoundingClientRect().width).toBeLessThanOrEqual(width)
 cleanup()
 render(<>{renderEvent({type:'agent_event',session_id:'fixture',client_event_id:'result',kind:'cron_command_result',ts:new Date().toISOString(),payload:{index:1,argv:['echo','health'],exit_code:7,timed_out:true,truncated:true,output:'<script>untrusted</script> [unsafe](https://example.com)',defined_by:'person'}},new Map())}</>)
 const summary=screen.getByText('Fixed command 1 · exit 7 · timed out · output truncated')
 fireEvent.click(summary)
 expect(screen.getByText('<script>untrusted</script> [unsafe](https://example.com)')).toBeTruthy()
 expect(screen.getByTestId('cron-command-result').querySelector('script, a')).toBeNull()
})
