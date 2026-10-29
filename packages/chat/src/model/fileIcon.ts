import type { FileIconKind } from '../components/fileIconPaths'

const extensions: Record<string, FileIconKind> = {}
const groups: Partial<Record<FileIconKind, string>> = {
  go: 'go', markdown: 'md markdown mdx', json: 'json jsonc json5', yaml: 'yaml yml',
  typescript: 'ts mts cts', javascript: 'js mjs cjs', react: 'jsx tsx', python: 'py pyi pyw',
  shell: 'sh bash zsh fish', powershell: 'ps1 psm1 psd1', docker: 'dockerfile', makefile: 'mk mak',
  lock: 'lock', image: 'png jpg jpeg gif webp avif bmp ico tiff tif heic', svg: 'svg',
  html: 'html htm', css: 'css', sass: 'sass scss', less: 'less', rust: 'rs', c: 'c h',
  cpp: 'cpp cc cxx c++ hpp hh hxx', csharp: 'cs csx', java: 'java class jar', kotlin: 'kt kts',
  swift: 'swift', ruby: 'rb erb gemspec', php: 'php', vue: 'vue', svelte: 'svelte',
  config: 'toml ini conf config properties env', database: 'sql sqlite sqlite3 db', csv: 'csv tsv',
  pdf: 'pdf', archive: 'zip gz tar tgz bz2 xz 7z rar zst', audio: 'mp3 wav ogg flac m4a aac',
  video: 'mp4 webm mov avi mkv', font: 'woff woff2 ttf otf eot', terraform: 'tf tfvars',
  lua: 'lua', xml: 'xml xsl xsd', notebook: 'ipynb',
}
for (const [kind, suffixes] of Object.entries(groups)) {
  for (const suffix of suffixes.split(' ')) extensions[suffix] = kind as FileIconKind
}
const names: Record<string, FileIconKind> = {
  'go.mod': 'go', 'go.work': 'go', 'go.sum': 'lock', 'go.work.sum': 'lock',
  'package.json': 'npm', 'package-lock.json': 'lock', 'npm-shrinkwrap.json': 'lock',
  'pnpm-lock.yaml': 'lock', 'bun.lockb': 'lock', 'tsconfig.json': 'typescript', 'jsconfig.json': 'javascript',
  '.bashrc': 'shell', '.bash_profile': 'shell', '.zshrc': 'shell', '.profile': 'shell',
  '.gitignore': 'git', '.gitattributes': 'git', '.gitmodules': 'git', '.gitconfig': 'git',
  'license': 'license', 'licence': 'license', 'copying': 'license',
  '.npmrc': 'npm', '.yarnrc': 'npm',
}
const folders: Record<string, FileIconKind> = {
  src: 'folder-src', source: 'folder-src', lib: 'folder-src',
  docs: 'folder-docs', doc: 'folder-docs', documentation: 'folder-docs',
  '.github': 'folder-github', test: 'folder-test', tests: 'folder-test', '__tests__': 'folder-test',
  node_modules: 'folder-node', public: 'folder-public', assets: 'folder-public', images: 'folder-public',
}
const lookup = (table: Record<string, FileIconKind>, key: string) => Object.hasOwn(table, key) ? table[key] : undefined

/** Pure filename detection, shared by every host. Accepts POSIX or Windows paths.
 * Exact names and special basenames take precedence over generic extensions.
 * `directory` is explicit: a directory called main.go is still a folder.
 */
export function detectFileIcon(name: string, directory = false): FileIconKind {
  const base = name.replace(/\\/g, '/').replace(/\/+$/, '').split('/').pop()?.toLowerCase() ?? ''
  if (directory) return lookup(folders, base) ?? 'folder'
  const exact = lookup(names, base)
  if (exact) return exact
  if (base.startsWith('dockerfile') || /^(docker-)?compose(?:\.[\w-]+)*\.ya?ml$/.test(base)) return 'docker'
  if (/^(?:gnu)?makefile(?:\.|$)/.test(base)) return 'makefile'
  if (/^(license|licence|copying)\./.test(base)) return 'license'
  if (base === '.env' || base.startsWith('.env.')) return 'config'
  return lookup(extensions, base.split('.').pop() ?? '') ?? 'file'
}
