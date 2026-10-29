// Which language a code block or a file is in, for highlighting. Pure and tiny: the highlighter
// itself (model/highlighter.ts) is a lazy chunk; this table is not.

/** The grammars the highlighter registers (highlight.js names). */
export type CodeLang =
  | 'javascript' | 'typescript' | 'go' | 'python' | 'bash' | 'json' | 'yaml' | 'sql'
  | 'dockerfile' | 'makefile' | 'gomod' | 'gosum' | 'mermaid' | 'csv'
  | 'xml' | 'css' | 'rust' | 'java' | 'c' | 'cpp' | 'markdown' | 'diff'

const ALIASES: Record<string, CodeLang> = {
  js: 'javascript', javascript: 'javascript', jsx: 'javascript', mjs: 'javascript', cjs: 'javascript',
  ts: 'typescript', typescript: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  go: 'go', golang: 'go',
  py: 'python', python: 'python', python3: 'python',
  sh: 'bash', bash: 'bash', shell: 'bash', zsh: 'bash', console: 'bash',
  json: 'json', jsonc: 'json', json5: 'json',
  yaml: 'yaml', yml: 'yaml',
  sql: 'sql',
  html: 'xml', xml: 'xml', svg: 'xml', htm: 'xml', xhtml: 'xml',
  css: 'css',
  rs: 'rust', rust: 'rust',
  java: 'java',
  c: 'c', h: 'c',
  cpp: 'cpp', 'c++': 'cpp', cc: 'cpp', cxx: 'cpp', hpp: 'cpp', hh: 'cpp',
  md: 'markdown', markdown: 'markdown',
  diff: 'diff', patch: 'diff',
  dockerfile: 'dockerfile', makefile: 'makefile', mk: 'makefile',
  gomod: 'gomod', gosum: 'gosum', mermaid: 'mermaid', mmd: 'mermaid', csv: 'csv', tsv: 'csv',
}

/** The language of a fence's info string ("go", "language-ts title=x"), or null when unknown. */
export function langFromInfo(info: string | undefined | null): CodeLang | null {
  if (!info) return null
  const first = info.trim().split(/\s+/)[0].replace(/^language-/, '').toLowerCase()
  return Object.prototype.hasOwnProperty.call(ALIASES, first) ? ALIASES[first] : null
}

/** The language of a file by its extension (code files arrive as view "text"), or null. */
export function langFromName(name: string): CodeLang | null {
  const base = name.split('/').at(-1)!.toLowerCase()
  if (base === 'go.mod') return 'gomod'
  if (base === 'go.sum') return 'gosum'
  if (base === 'dockerfile' || base.startsWith('dockerfile.')) return 'dockerfile'
  if (base === 'makefile' || base === 'gnumakefile') return 'makefile'
  const dot = base.lastIndexOf('.')
  if (dot < 0) return null
  const ext = base.slice(dot + 1)
  return Object.prototype.hasOwnProperty.call(ALIASES, ext) ? ALIASES[ext] : null
}

/** What the viewer header calls a language. */
export function langLabel(lang: CodeLang | null): string {
  switch (lang) {
    case null: return 'Text'
    case 'javascript': return 'JavaScript'
    case 'typescript': return 'TypeScript'
    case 'go': return 'Go'
    case 'python': return 'Python'
    case 'bash': return 'Shell'
    case 'json': return 'JSON'
    case 'yaml': return 'YAML'
    case 'sql': return 'SQL'
    case 'xml': return 'HTML/XML'
    case 'css': return 'CSS'
    case 'rust': return 'Rust'
    case 'java': return 'Java'
    case 'c': return 'C'
    case 'cpp': return 'C++'
    case 'markdown': return 'Markdown'
    case 'diff': return 'Diff'
    case 'dockerfile': return 'Dockerfile'
    case 'makefile': return 'Makefile'
    case 'gomod': return 'Go module'
    case 'gosum': return 'Go checksums'
    case 'mermaid': return 'Mermaid'
    case 'csv': return 'CSV'
  }
}
