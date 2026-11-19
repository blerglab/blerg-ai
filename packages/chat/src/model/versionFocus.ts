import { isValidElement, type ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { parsePatch } from './patch'

function renderedText(node: ReactNode): string {
  if (typeof node === 'string' || typeof node === 'number') return String(node)
  if (Array.isArray(node)) return node.map(renderedText).join('')
  if (isValidElement<{ children?: ReactNode }>(node)) return renderedText(node.props.children)
  return ''
}

// Keep the original patch line numbers while narrowing to a requested passage.
export function focusVersionPatch(patch: string, quote: string): string | null {
  const file = parsePatch(patch).files[0]
  const lines = file?.hunks[0]?.lines
  if (!lines || !quote.trim()) return null
  const needles = quote.split('\n').map(s => s.trim()).filter(Boolean)
  const hits = lines.flatMap((l, i) => {
    if (needles.some(n => l.text.includes(n))) return [i]
    if (!/[*_~\x60[<#&]/.test(l.text)) return []
    const rendered = renderedText(Markdown({ children: l.text, remarkPlugins: [remarkGfm] }))
    return needles.some(n => rendered.includes(n)) ? [i] : []
  })
  if (!hits.length) return null
  const start = Math.max(0, hits[0] - 5), end = Math.min(lines.length, hits.at(-1)! + 6)
  const selected = lines.slice(start, end)
  const oldStart = selected.find(l => l.old !== undefined)?.old ?? 0
  const newStart = selected.find(l => l.new !== undefined)?.new ?? 0
  const oldCount = selected.filter(l => l.kind === 'ctx' || l.kind === 'del').length
  const newCount = selected.filter(l => l.kind === 'ctx' || l.kind === 'add').length
  return `--- ${JSON.stringify(`a/${file.path}`)}\n+++ ${JSON.stringify(`b/${file.path}`)}\n@@ -${oldStart},${oldCount} +${newStart},${newCount} @@\n${selected.map(l => (l.kind === 'ctx' ? ' ' : l.kind === 'del' ? '-' : l.kind === 'add' ? '+' : '\\') + l.text).join('\n')}\n`
}
