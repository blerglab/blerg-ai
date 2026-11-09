import type { CSSProperties } from 'react'
import { detectFileIcon } from '../model/fileIcon'
import { fileIconPaths, type FileIconKind } from './fileIconPaths'
import './FileIcon.css'

// Seti's quiet single-color palette, as the --chat-icon-* tokens. Mixing with the host's
// foreground below preserves its language hues while providing contrast in either theme.
const BLUE = 'var(--chat-icon-blue)', YELLOW = 'var(--chat-icon-yellow)', PURPLE = 'var(--chat-icon-purple)'
const GREEN = 'var(--chat-icon-green)', ORANGE = 'var(--chat-icon-orange)', PINK = 'var(--chat-icon-pink)'
const RED = 'var(--chat-icon-red)', TAN = 'var(--chat-icon-tan)'
const colors: Partial<Record<FileIconKind, string>> = {
  go: BLUE, markdown: BLUE, json: YELLOW, yaml: PURPLE,
  typescript: BLUE, javascript: YELLOW, react: BLUE, python: YELLOW,
  shell: GREEN, powershell: BLUE, docker: BLUE, makefile: ORANGE, lock: PURPLE,
  image: PURPLE, svg: YELLOW, html: ORANGE, css: BLUE, sass: PINK, less: BLUE,
  rust: ORANGE, c: BLUE, cpp: PURPLE, csharp: BLUE, java: RED, kotlin: ORANGE,
  swift: ORANGE, ruby: RED, php: PURPLE, vue: GREEN, svelte: ORANGE,
  npm: RED, database: PINK, csv: GREEN, pdf: RED, archive: ORANGE,
  audio: PURPLE, video: PINK, font: RED, git: ORANGE, license: YELLOW,
  terraform: PURPLE, lua: BLUE, xml: ORANGE, notebook: ORANGE,
  folder: TAN, 'folder-src': BLUE, 'folder-docs': BLUE, 'folder-github': PURPLE,
  'folder-test': GREEN, 'folder-node': RED, 'folder-public': PURPLE,
}

export interface FileIconProps {
  name: string
  directory?: boolean
  expanded?: boolean
  label?: string
  className?: string
}

/** One decorative SVG for files and folders in chat, runner, and board surfaces. */
export function FileIcon({ name, directory = false, expanded = false, label, className = '' }: FileIconProps) {
  const kind = detectFileIcon(name, directory)
  const color = colors[kind]
  return <svg className={`file-icon artifact-glyph ${className}`} data-file-icon={kind}
    viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false"
    style={color ? { '--chat-file-icon-color': color } as CSSProperties : undefined}>
    {label && <title>{label}</title>}
    {directory ? <>
      <path d={expanded ? 'M2 5V3h4l2 2h6v2M2 7h12l-2 6H2z' : 'M2 13V3h4l2 2h6v8z'} fill="none" stroke="currentColor" strokeWidth="1" strokeLinejoin="round" />
      {kind !== 'folder' && <path d={fileIconPaths[kind]} fill={kind === 'folder-github' || kind === 'folder-node' || kind === 'folder-public' ? 'currentColor' : 'none'} stroke="currentColor" strokeWidth=".8" strokeLinecap="round" strokeLinejoin="round" />}
    </> : <path d={fileIconPaths[kind]} fill="currentColor" />}
  </svg>
}
