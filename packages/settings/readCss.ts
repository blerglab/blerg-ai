// Node built-ins through process.getBuiltinModule, typed here: the apps that run this (core, board, the
// chat package) do not all have Node's type definitions, and core/web installs on its own in CI.
interface Fs {
  readdirSync(dir: string, options: { withFileTypes: true }): { name: string, isDirectory(): boolean }[]
  readFileSync(path: string, encoding: 'utf8'): string
}
interface Path { join(...parts: string[]): string, resolve(...parts: string[]): string }
const node = (globalThis as unknown as { process: { getBuiltinModule(id: string): unknown } }).process

/**
 * Every .css file under these directories (relative to the working directory, the app's root under
 * `npm test`), by path. Tests read CSS from disk: Vitest empties CSS imports, `?raw` too.
 */
export function readCss(...dirs: string[]): Record<string, string> {
  const fs = node.getBuiltinModule('node:fs') as Fs
  const path = node.getBuiltinModule('node:path') as Path
  const out: Record<string, string> = {}
  const walk = (dir: string) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const file = path.join(dir, entry.name)
      if (entry.isDirectory()) { if (entry.name !== 'node_modules' && entry.name !== 'dist') walk(file) }
      else if (entry.name.endsWith('.css')) out[file] = fs.readFileSync(file, 'utf8')
    }
  }
  dirs.forEach(dir => walk(path.resolve(dir)))
  return out
}
