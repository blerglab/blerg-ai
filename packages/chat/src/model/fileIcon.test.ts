import { describe, expect, it } from 'vitest'
import { detectFileIcon } from './fileIcon'

describe('file icon detection', () => {
  it.each([
    ['main.go', 'go'], ['go.mod', 'go'], ['go.sum', 'lock'], ['go.work', 'go'],
    ['README.md', 'markdown'], ['notes.mdx', 'markdown'], ['package.json', 'npm'], ['tsconfig.json', 'typescript'],
    ['a.json', 'json'], ['a.jsonc', 'json'], ['a.yaml', 'yaml'], ['a.yml', 'yaml'],
    ['app.ts', 'typescript'], ['app.d.ts', 'typescript'], ['app.tsx', 'react'], ['app.jsx', 'react'],
    ['app.js', 'javascript'], ['app.mjs', 'javascript'], ['app.cjs', 'javascript'], ['app.py', 'python'],
    ['run.sh', 'shell'], ['.bashrc', 'shell'], ['script.ps1', 'powershell'],
    ['Dockerfile', 'docker'], ['Dockerfile.dev', 'docker'], ['DockerfileProduction', 'docker'],
    ['compose.yaml', 'docker'], ['docker-compose.test.yml', 'docker'], ['app.dockerfile', 'docker'],
    ['Makefile', 'makefile'], ['GNUmakefile', 'makefile'], ['Makefile.local', 'makefile'], ['build.mk', 'makefile'],
    ['package-lock.json', 'lock'], ['pnpm-lock.yaml', 'lock'], ['yarn.lock', 'lock'], ['Cargo.lock', 'lock'], ['uv.lock', 'lock'],
    ['photo.PNG', 'image'], ['drawing.svg', 'svg'], ['photo.avif', 'image'],
    ['index.html', 'html'], ['style.css', 'css'], ['style.scss', 'sass'], ['style.less', 'less'],
    ['main.rs', 'rust'], ['main.c', 'c'], ['main.hpp', 'cpp'], ['main.cs', 'csharp'], ['main.java', 'java'],
    ['main.kt', 'kotlin'], ['main.swift', 'swift'], ['main.rb', 'ruby'], ['index.php', 'php'],
    ['App.vue', 'vue'], ['App.svelte', 'svelte'], ['.env.local', 'config'], ['data.sql', 'database'],
    ['data.csv', 'csv'], ['book.pdf', 'pdf'], ['archive.tar.gz', 'archive'], ['clip.mp4', 'video'],
    ['clip.mp3', 'audio'], ['font.woff2', 'font'], ['.gitignore', 'git'], ['LICENSE.txt', 'license'],
    ['main.tf', 'terraform'], ['script.lua', 'lua'], ['file.xml', 'xml'], ['analysis.ipynb', 'notebook'],
    ['src/main.go', 'go'], ['C:\\project\\main.PY', 'python'],
    ['unknown.custom', 'file'], ['', 'file'], ['__proto__', 'file'], ['constructor', 'file'], ['a.toString', 'file'],
  ])('%s → %s', (name, kind) => expect(detectFileIcon(name)).toBe(kind))

  it.each([
    ['src', 'folder-src'], ['lib', 'folder-src'], ['docs', 'folder-docs'], ['.github', 'folder-github'],
    ['test', 'folder-test'], ['__tests__', 'folder-test'], ['node_modules', 'folder-node'], ['public', 'folder-public'],
    ['other', 'folder'], ['main.go', 'folder'], ['project/docs/', 'folder-docs'],
  ])('detects the folder %s independently of file extensions', (name, kind) => expect(detectFileIcon(name, true)).toBe(kind))
})
