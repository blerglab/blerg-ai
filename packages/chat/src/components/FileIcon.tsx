import type { CSSProperties } from 'react'
import { detectFileIcon } from '../model/fileIcon'
import { fileIconPaths, type FileIconKind } from './fileIconPaths'
import './FileIcon.css'

// Seti's quiet single-color palette. Mixing with the host's foreground below
// preserves its language hues while providing contrast in either theme.
const colors: Partial<Record<FileIconKind, string>> = {
  go: '#519aba', markdown: '#519aba', json: '#cbcb41', yaml: '#a074c4',
  typescript: '#519aba', javascript: '#cbcb41', react: '#519aba', python: '#cbcb41',
  shell: '#8dc149', powershell: '#519aba', docker: '#519aba', makefile: '#e37933', lock: '#a074c4',
  image: '#a074c4', svg: '#cbcb41', html: '#e37933', css: '#519aba', sass: '#f55385', less: '#519aba',
  rust: '#e37933', c: '#519aba', cpp: '#a074c4', csharp: '#519aba', java: '#cc3e44', kotlin: '#e37933',
  swift: '#e37933', ruby: '#cc3e44', php: '#a074c4', vue: '#8dc149', svelte: '#e37933',
  npm: '#cc3e44', database: '#f55385', csv: '#8dc149', pdf: '#cc3e44', archive: '#e37933',
  audio: '#a074c4', video: '#f55385', font: '#cc3e44', git: '#e37933', license: '#cbcb41',
  terraform: '#a074c4', lua: '#519aba', xml: '#e37933', notebook: '#e37933',
  folder: '#b8a36a', 'folder-src': '#519aba', 'folder-docs': '#519aba', 'folder-github': '#a074c4',
  'folder-test': '#8dc149', 'folder-node': '#cc3e44', 'folder-public': '#a074c4',
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
