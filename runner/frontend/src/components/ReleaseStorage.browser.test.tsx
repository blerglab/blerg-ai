import {render,screen,within} from '@testing-library/react'
import {it,expect,vi,afterEach} from 'vitest'
import {page,userEvent} from 'vitest/browser'
import ReleaseStorage from './ReleaseStorage'
import {setTheme} from '../../../../packages/settings/theme'
import '../index.css'
import {apiFetch} from '../apiFetch'
vi.mock('../apiFetch',()=>({apiFetch:vi.fn()}))
afterEach(()=>vi.resetAllMocks())
for(const theme of ['dark','colorblind-dark'] as const)for(const width of [390,3840])it(`shows storage confirmation at ${width}px (${theme})`,async()=>{
 setTheme(theme)
 await page.viewport(width,900)
 vi.mocked(apiFetch).mockResolvedValue(new Response(JSON.stringify({digest:'a'.repeat(64),files:['app/private/local-notes.txt','app/.pytest_cache/v/cache/nodeids'],commits:['app: refs/heads/unsaved 1234567'],file_count:702,commit_count:3,truncated:true,released:false})))
 render(<div style={{padding:24,display:'flex',justifyContent:'flex-end'}}><ReleaseStorage sessionId="stopped"/></div>)
 await userEvent.click(screen.getByRole('button',{name:'Release storage'}))
 const dialog=await screen.findByRole('alertdialog',{name:'Release this session’s storage?'})
 expect(dialog).toHaveTextContent('app/private/local-notes.txt')
 expect(dialog).toHaveTextContent('The reflog is not kept.')
 expect(dialog).toHaveTextContent('2 of 702')
 expect(within(dialog).getByRole('button',{name:'Cancel'})).toHaveFocus()
 const rect=dialog.getBoundingClientRect();expect(rect.left).toBeGreaterThanOrEqual(0);expect(rect.right).toBeLessThanOrEqual(width)
 if(import.meta.env.VITE_GRAPH_SHOTS_DIR)await page.screenshot({path:`${import.meta.env.VITE_GRAPH_SHOTS_DIR}/release-storage-${width}${theme==='dark'?'':'-colorblind'}.png`})
 await userEvent.keyboard('{Escape}');expect(screen.queryByRole('alertdialog')).toBeNull();expect(apiFetch).toHaveBeenCalledTimes(1)
})
