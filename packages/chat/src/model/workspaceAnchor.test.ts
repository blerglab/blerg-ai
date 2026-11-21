import { describe, expect, it } from 'vitest'
import { createWorkspaceAnchor, projectWorkspaceAnchor } from './workspaceAnchor'
import { parseReviewFile } from './review'

describe('workspace line anchors', () => {
  it('binds the file, inclusive lines and SHA-256 of the source', async () => {
    const anchor = await createWorkspaceAnchor('src/app.ts', 'first\nsecond\nthird', 2, 3)
    expect(anchor).toMatchObject({kind: 'text', quote: 'second\nthird', workspace: {path: 'src/app.ts', start: 2, end: 3}})
    expect(anchor.workspace?.contentHash).toMatch(/^[a-f0-9]{64}$/)
    const parsed = parseReviewFile(JSON.stringify({version: 1, file: {name: 'Workspace files', artifactId: 'workspace'}, requests: [{id:'r', anchor, text:'Change it', status:'open', createdAt:'now'}]}))
    expect(parsed?.requests[0].anchor).toEqual(anchor)
  })
  it('moves an unchanged passage after small edits and refuses missing or ambiguous code', async () => {
    const anchor = await createWorkspaceAnchor('app.ts', 'first\nsecond\nthird', 2, 2)
    expect(await projectWorkspaceAnchor(anchor, 'intro\nfirst\nsecond\nthird')).toEqual({start:3,end:3,moved:true})
    expect(await projectWorkspaceAnchor(anchor, 'first\nchanged\nthird')).toBeNull()
    expect(await projectWorkspaceAnchor(anchor, 'second\nfirst\nsecond\nthird')).toBeNull()
  })
  it('keeps repeated lines at their exact position when the source is unchanged', async () => {
    const source = 'same\nsame'
    const anchor = await createWorkspaceAnchor('app.ts', source, 2, 2)
    expect(await projectWorkspaceAnchor(anchor, source)).toEqual({start:2,end:2,moved:false})
  })
  it('rejects invalid ranges and paths', async () => {
    for (const path of ['../secret', '/secret', 'a/../../secret', 'a\u0000b']) {
      await expect(createWorkspaceAnchor(path, 'one', 1, 1)).rejects.toThrow()
    }
    await expect(createWorkspaceAnchor('app.ts', 'one', 1, 2)).rejects.toThrow()
  })
})

it('rejects malformed persisted workspace anchors rather than dropping their provenance', () => {
  for (const workspace of [
    {path:'../secret',start:1,end:1,contentHash:'a'.repeat(64)},
    {path:'app.ts',start:2,end:1,contentHash:'a'.repeat(64)},
    {path:'app.ts',start:1,end:1,contentHash:'not a hash'},
  ]) {
    const parsed = parseReviewFile(JSON.stringify({version:1,file:{name:'Workspace files',artifactId:'workspace'},requests:[{id:'r',anchor:{kind:'text',quote:'one',workspace},text:'Change',status:'open',createdAt:'now'}]}))
    expect(parsed).toBeNull()
  }
})
