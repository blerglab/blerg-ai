import { visualFile } from './visualKind'

/** Source is available only for textual previews, not binary media. */
export function hasRenderedText(name: string, view: string, text: string): boolean {
  return ['markdown', 'html', 'csv', 'json', 'diff'].includes(view) || visualFile(name, view, text) !== null
}
