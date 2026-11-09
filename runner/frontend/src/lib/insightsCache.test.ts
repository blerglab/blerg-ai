import {expect,it} from 'vitest'
import {cachedInsights,clearInsightsCache,insightsCacheKey,rememberInsights} from './insightsCache'
import {richInsights} from '../test/insightsFixture'

it('retains a bounded cache for remounts and clears it on identity change',()=>{
 const key=insightsCacheKey('person-a','summary|workspace|7d')
 rememberInsights(key,richInsights)
 expect(cachedInsights(key)).toBe(richInsights)
 window.dispatchEvent(new Event('blerg-identity-changed'))
 expect(cachedInsights(key)).toBeNull()
 for(let i=0;i<25;i++)rememberInsights(String(i),i)
 expect(cachedInsights('0')).toBeNull()
 expect(cachedInsights('24')).toBe(24)
 clearInsightsCache()
})
