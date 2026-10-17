// The syntax highlighter itself: lowlight (highlight.js grammars, MIT/BSD-3) with the common
// languages registered. This module is imported ONLY through loadHighlighter()'s dynamic import,
// so Vite splits it (and the grammars) into a chunk of its own and a transcript's first paint
// never waits for it.
//
// It produces tokens, never markup: lowlight's tree is flattened to plain {text} / {cls, kids}
// nodes that React draws as <span>s, so nothing a model wrote is ever parsed as HTML.
import { createLowlight } from 'lowlight'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import go from 'highlight.js/lib/languages/go'
import python from 'highlight.js/lib/languages/python'
import bash from 'highlight.js/lib/languages/bash'
import json from 'highlight.js/lib/languages/json'
import yaml from 'highlight.js/lib/languages/yaml'
import sql from 'highlight.js/lib/languages/sql'
import xml from 'highlight.js/lib/languages/xml'
import css from 'highlight.js/lib/languages/css'
import rust from 'highlight.js/lib/languages/rust'
import java from 'highlight.js/lib/languages/java'
import c from 'highlight.js/lib/languages/c'
import cpp from 'highlight.js/lib/languages/cpp'
import markdown from 'highlight.js/lib/languages/markdown'
import type { CodeLang } from './codeLang'
import type { Token } from './highlight'

const lowlight = createLowlight({ javascript, typescript, go, python, bash, json, yaml, sql, xml, css, rust, java, c, cpp, markdown })

type Tree = ReturnType<typeof lowlight.highlight>
type Node = Tree['children'][number]

function convert(nodes: Node[]): Token[] {
  const out: Token[] = []
  for (const n of nodes) {
    if (n.type === 'text') out.push({ text: n.value })
    else if (n.type === 'element') {
      const cls = Array.isArray(n.properties?.className) ? n.properties.className.map(String).join(' ') : ''
      out.push({ cls, kids: convert(n.children as Node[]) })
    }
  }
  return out
}

export function highlightTokens(lang: CodeLang, text: string): Token[] {
  const tree = lowlight.highlight(lang, text)
  return convert(tree.children)
}
