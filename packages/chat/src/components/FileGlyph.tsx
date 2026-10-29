import { FileIcon } from './FileIcon'

// Retain the public API for existing hosts; all rendering goes through FileIcon.
const extensions: Record<string, string> = { markdown: 'md', image: 'png', text: 'txt', audio: 'mp3', video: 'mp4' }
/** @deprecated Pass the actual filename to FileIcon for language detection. */
export function FileGlyph({ view, label }: { view: string; label: string }) {
  const extension = Object.hasOwn(extensions, view) ? extensions[view] : view
  return <FileIcon name={`file.${extension}`} label={label} />
}
/** @deprecated Use FileIcon with directory and the actual folder name. */
export function FolderGlyph({ expanded = false }: { expanded?: boolean }) {
  return <FileIcon name="" directory expanded={expanded} />
}
