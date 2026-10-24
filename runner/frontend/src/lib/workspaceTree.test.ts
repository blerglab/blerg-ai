import { expect, it } from 'vitest'
import { pathCodes, withDeletedEntries, workspaceChanges } from './workspaceTree'
it('projects repository-relative statuses, rolls up ancestors, inherits untracked folders and retains deleted paths', () => {
 const changes = workspaceChanges({ app: { branch: 'main', changed: 2, untracked: 1, partial: false, files: [{ path: 'src/a.ts', code: 'M' }, { path: 'removed/old.ts', code: 'D' }, { path: 'assets', code: 'U', directory: true }] } })
 expect(pathCodes({ name:'src',path:'app/src',directory:true }, changes)).toEqual(['M'])
 expect(pathCodes({ name:'a.ts',path:'app/src/a.ts',directory:false }, changes)).toEqual(['M'])
 expect(pathCodes({ name:'logo.png',path:'app/assets/logo.png',directory:false }, changes)).toEqual(['U'])
 expect(pathCodes({ name:'other',path:'other/src/a.ts',directory:false }, changes)).toEqual([])
 expect(withDeletedEntries([], 'app', changes)).toEqual([{ name:'removed',path:'app/removed',directory:true,deleted:true }])
 expect(withDeletedEntries([], 'app/removed', changes)).toEqual([{ name:'old.ts',path:'app/removed/old.ts',directory:false,deleted:true }])
 expect(workspaceChanges({ app: { branch:'main',changed:1,untracked:0,partial:true } })).toEqual([])
})
