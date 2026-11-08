import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import ts from 'typescript'
import { expect, it } from 'vitest'

it('keeps every browser fixture that changes touch emulation on the isolated instance', () => {
  // Read the actual configuration without importing it (and starting its fixture plugins).
  const config = ts.createSourceFile('vitest.config.ts', readFileSync('vitest.config.ts', 'utf8'), ts.ScriptTarget.Latest, true)
  const declaration = config.statements
    .filter(ts.isVariableStatement)
    .flatMap(statement => [...statement.declarationList.declarations])
    .find(node => ts.isIdentifier(node.name) && node.name.text === 'touchEmulationFiles')
  const initializer = declaration?.initializer
  if (!initializer || !ts.isArrayLiteralExpression(initializer) || !initializer.elements.every(ts.isStringLiteral)) {
    throw new Error('Keep touchEmulationFiles in vitest.config.ts as a literal array of browser fixture paths so the isolation guard can audit it.')
  }
  const isolated = new Set(initializer.elements.filter(ts.isStringLiteral).map(element => element.text))
  const commands = ['Emulation.setTouchEmulationEnabled', 'Emulation.setEmitTouchEventsForMouse']
  const unisolated: string[] = []
  for (const entry of readdirSync('src', { recursive: true, withFileTypes: true })) {
    if (!entry.isFile() || !/\.browser\.test\.[^/\\]+$/.test(entry.name)) continue
    const path = join(entry.parentPath, entry.name).replaceAll('\\', '/')
    const source = readFileSync(path, 'utf8')
    for (const command of commands) {
      if (source.includes(command) && !isolated.has(path)) unisolated.push(`${path}: ${command}`)
    }
  }
  expect(unisolated, 'Browser fixtures that change touch emulation must be listed in touchEmulationFiles in vitest.config.ts; their reused pages lose desktop hover capability.').toEqual([])
})
