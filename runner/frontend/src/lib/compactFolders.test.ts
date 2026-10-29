import { expect, it } from 'vitest'
import * as tree from './workspaceTree'
import type { TreeEntry } from './workspaceTree'
const dir = (path: string): TreeEntry => ({ path, name: path.split('/').at(-1)!, directory: true })
const dev = dir('dev'), ll = dir('dev/ll'), api = dir('dev/ll/api')
const page = (entries: TreeEntry[], truncated = false) => ({ entries, truncated })
const chain = (pages: Record<string, ReturnType<typeof page>>, enabled = true, roots: string[] = []) => tree.compactFolderChain(dev, pages, new Set(roots), enabled).map(e => e.path)
it('compacts a loaded chain and stops at an unknown listing', () => {
  expect(chain({ dev: page([ll]), 'dev/ll': page([api]) })).toEqual(['dev', 'dev/ll', 'dev/ll/api'])
})
it('stops at a file alongside a folder', () => {
  expect(chain({ dev: page([ll]), 'dev/ll': page([api, { name: 'a', path: 'dev/ll/a', directory: false }]) })).toEqual(['dev', 'dev/ll'])
})
it('stops at a second folder', () => {
  expect(chain({ dev: page([ll]), 'dev/ll': page([api, dir('dev/ll/other')]) })).toEqual(['dev', 'dev/ll'])
})
it('stops at an empty folder', () => { expect(chain({ dev: page([]) })).toEqual(['dev']) })
it('keeps member roots as standalone rows', () => {
  expect(chain({ dev: page([ll]), 'dev/ll': page([api]) }, true, ['dev/ll'])).toEqual(['dev'])
  expect(chain({ dev: page([ll]) }, true, ['dev'])).toEqual(['dev'])
})
it('keeps plain rows with the setting off', () => { expect(chain({ dev: page([ll]) }, false)).toEqual(['dev']) })
it('does not infer one child from a partial listing', () => { expect(chain({ dev: page([ll], true) })).toEqual(['dev']) })

it('keeps repository folders standalone, including discovered repositories inside another folder', () => {
  expect(chain({ dev: page([{ ...ll, repository: true }]), 'dev/ll': page([api]) })).toEqual(['dev'])
  expect(tree.compactFolderChain({ ...dev, repository: true }, { dev: page([ll]) }, new Set(), true)).toEqual([{ ...dev, repository: true }])
})
