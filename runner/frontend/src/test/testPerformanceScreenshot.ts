import {mkdir} from 'node:fs/promises'
import {resolve} from 'node:path'
import type {BrowserCommandContext} from 'vitest/node'

export async function testPerformanceScreenshot({page,iframe}:BrowserCommandContext,width:number,state:string) {
 const directory=process.env.VITE_BLG402_SHOTS_DIR
 if(!directory||![390,3840].includes(width)||!['trends','details','colorblind','scan','findings'].includes(state))throw new Error('Invalid test performance screenshot')
 await page.setViewportSize({width:4200,height:2400})
 await iframe.locator('html').evaluate(()=>{const container=window.frameElement?.parentElement;if(container){container.style.transform='scale(1)';container.setAttribute('data-scale','1')}})
 const box=await iframe.locator('html').boundingBox()
 if(!box)throw new Error('Missing test performance viewport')
 await mkdir(directory,{recursive:true})
 await page.screenshot({path:resolve(directory,`${state}-${width}.png`),clip:{x:box.x,y:box.y,width,height:width===390?844:1600}})
}
