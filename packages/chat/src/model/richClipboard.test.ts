import { describe, expect, it } from 'vitest'
import { codePayload, copyPayload, htmlToMarkdown, insideOpenFence, looksLikeMarkdown, markdownForPaste } from './richClipboard'

const md = (html: string) => htmlToMarkdown(html).markdown
const frag = (html: string) => { const d = document.createElement('div'); d.innerHTML = html; return d }

describe('HTML to Markdown', () => {
  it('converts the inline and block basics', () => {
    expect(md('<h2>Title</h2><p>a <strong>bold</strong>, <em>it</em>, <del>gone</del> and <code>x()</code></p><hr><blockquote><p>q</p></blockquote>'))
      .toBe('## Title\n\na **bold**, *it*, ~~gone~~ and `x()`\n\n---\n\n> q')
  })

  it('keeps the language of a code block', () => {
    expect(md('<pre><code class="language-go">fmt.Println(1)\n</code></pre>')).toBe('```go\nfmt.Println(1)\n```')
    expect(md('<div class="highlight highlight-source-js"><pre class="notranslate"><code>let a</code></pre></div>')).toBe('```js\nlet a\n```')
    expect(md('<pre><code>a ``` b</code></pre>')).toBe('````\na ``` b\n````')
  })

  it('nests lists and handles task lists and numbers', () => {
    expect(md('<ul><li>a<ul><li>b</li></ul></li><li>c</li></ul>')).toBe('- a\n  - b\n- c')
    expect(md('<ol start="3"><li>x</li><li>y</li></ol>')).toBe('3. x\n4. y')
    expect(md('<ul class="contains-task-list"><li class="task-list-item"><input type="checkbox" checked disabled> done</li><li><input type="checkbox"> todo</li></ul>'))
      .toBe('- [x] done\n- [ ] todo')
  })

  it('keeps only http, https and mailto links', () => {
    expect(md('<a href="https://e.test/a(b)">site</a> <a href="javascript:alert(1)">bad</a> <a href="mailto:a@e.test">m</a> <a href="/rel">r</a>'))
      .toBe('[site](https://e.test/a%28b%29) bad [m](mailto:a@e.test) r')
  })

  it('writes a GFM table, flattening cell content', () => {
    expect(md('<table><thead><tr><th>A</th><th>B</th></tr></thead><tbody><tr><td><p>1</p><p>x|y</p></td><td>2</td></tr></tbody></table>'))
      .toBe('| A | B |\n| --- | --- |\n| 1 x\\|y | 2 |')
  })

  it('drops images, scripts, styles, iframes, forms, comments and raw HTML', () => {
    const { markdown } = htmlToMarkdown('<!--c--><style>p{}</style><script>alert(1)</script><p>keep <img src="http://t.test/x.png" alt="pixel"> this</p><iframe src="http://e.test"></iframe><form><input value="secret"><button>Go</button></form><p>&lt;b&gt;text&lt;/b&gt;</p>')
    expect(markdown).toBe('keep this\n\n\\<b>text\\</b>')
    expect(markdown).not.toMatch(/alert|pixel|secret|Go|http/)
  })

  it('escapes what would turn into formatting, and nothing else', () => {
    expect(md('<p>2 * 3 and *x* and snake_case and _y_ and C:\\Users and arr[0]</p>')).toBe('2 * 3 and \\*x\\* and snake_case and \\_y\\_ and C:\\Users and arr[0]')
    expect(md('<p># not a heading</p>')).toBe('\\# not a heading')
  })
})

describe('what sources send', () => {
  it('Google Docs: the guid wrapper is not bold; styled spans are; nested lists sit beside the item', () => {
    const html = '<meta charset="utf-8"><b style="font-weight:normal;" id="docs-internal-guid-1"><h1 dir="ltr"><span style="font-weight:700">Plan</span></h1>'
      + '<p dir="ltr"><span style="font-weight:400">a </span><span style="font-weight:700">key</span><span style="font-style:italic"> note</span></p>'
      + '<ul><li dir="ltr"><p dir="ltr"><span>one</span></p></li><ul><li dir="ltr"><p dir="ltr"><span>inner</span></p></li></ul></ul></b>'
    expect(md(html)).toBe('# Plan\n\na **key** *note*\n\n- one\n  - inner')
  })

  it('Slack: bold, code, a list and a link', () => {
    const html = '<div class="p-rich_text_section"><b data-stringify-type="bold">Deploy</b> done: <code data-stringify-type="code">v1</code> see <a href="https://e.test/r">run</a></div><ul><li>first</li><li>second</li></ul>'
    expect(md(html)).toBe('**Deploy** done: `v1` see [run](https://e.test/r)\n\n- first\n- second')
  })

  it('GitHub: a rendered issue comment', () => {
    const html = '<h3>Steps</h3><ol><li>Run <code>make</code></li><li>Check</li></ol><div class="highlight highlight-source-shell"><pre><code>make test</code></pre></div>'
    expect(md(html)).toBe('### Steps\n\n1. Run `make`\n2. Check\n\n```shell\nmake test\n```')
  })

  it('a JIRA-like page: table cells with paragraphs and lozenges', () => {
    const html = '<table class="confluenceTable"><tbody><tr><th class="confluenceTh">Key</th><th>Status</th></tr><tr><td><a class="issue-link" href="https://jira.example.test/browse/AB-1">AB-1</a></td><td><span class="aui-lozenge">Done</span></td></tr></tbody></table>'
    expect(md(html)).toBe('| Key | Status |\n| --- | --- |\n| [AB-1](https://jira.example.test/browse/AB-1) | Done |')
  })

  it('VS Code and terminals: spans only is plain text, so nothing is converted', () => {
    const html = '<meta charset="utf-8"><div style="color:#ccc;font-family:Menlo"><div><span style="color:#569cd6">const</span><span> a = 1;</span></div><div><span>f(a)</span></div></div>'
    expect(markdownForPaste(html, 'const a = 1;\nf(a)')).toBeNull()
  })

  it('a web page: converted, with images and scripts dropped', () => {
    const html = '<h2>News</h2><img src="http://t.test/p.gif"><script>x()</script><p>Read <a href="https://e.test/n">more</a></p>'
    expect(markdownForPaste(html, 'News\nRead more')).toBe('## News\n\nRead [more](https://e.test/n)')
  })

  it('uses the plain text when it already is Markdown', () => {
    const plain = '# Title\n\n```js\nx\n```'
    expect(looksLikeMarkdown(plain)).toBe(true)
    expect(markdownForPaste('<h1>Title</h1><pre><code>x</code></pre>', plain)).toBeNull()
    expect(looksLikeMarkdown('1. one\n2. two')).toBe(false)
  })

  it('never throws', () => {
    expect(markdownForPaste('<<<>>', 'x')).toBeNull()
    expect(markdownForPaste('', 'x')).toBeNull()
  })
})

describe('copying out of the chat', () => {
  const rendered = '<div class="md"><h2>Plan</h2><p>See <a href="https://e.test/x" target="_blank" rel="noopener" onclick="x()">the <strong>doc</strong></a>.</p>'
    + '<ul><li>one</li><li>two<ul><li>deep</li></ul></li></ul>'
    + '<div class="md-codeblock"><button class="md-copy">Copy</button><pre data-lang="go"><code><span class="hl-k">func</span> main() {}\n</code></pre><div class="md-codeblock-actions"><button>Open</button></div></div></div>'
    + '<div class="card-foot"><time>2m ago</time></div>'

  it('keeps structure, drops chrome, and is sanitised', () => {
    const { markdown, html } = copyPayload(frag(rendered))
    expect(markdown).toBe('## Plan\n\nSee [the **doc**](https://e.test/x).\n\n- one\n- two\n  - deep\n\n```go\nfunc main() {}\n```')
    expect(html).toContain('<h2>Plan</h2>')
    expect(html).toContain('<a href="https://e.test/x">the <strong>doc</strong></a>')
    expect(html).toContain('<pre><code class="language-go">func main() {}</code></pre>')
    expect(html).not.toMatch(/onclick|target|<button|<time|Copy|Open|2m ago|hl-k/)
  })

  it('copies code text exactly', () => {
    expect(codePayload('a < b\n  c', 'go')).toEqual({ markdown: 'a < b\n  c', html: '<pre><code class="language-go">a &lt; b\n  c</code></pre>' })
  })
})

describe('bounds and literal text', () => {
  it('skips a huge or deeply nested paste, and does it fast', () => {
    const t = Date.now()
    expect(markdownForPaste('<p><b>x</b></p>'.repeat(700_000), 'x')).toBeNull() // ~10 MB
    expect(markdownForPaste('<ul><li>a'.repeat(3000), 'a')).toBeNull()
    expect(markdownForPaste('<p><b>x</b></p>'.repeat(30_000), 'x')).toBeNull() // over the node budget
    expect(Date.now() - t).toBeLessThan(2000)
  })

  it('still converts reasonable nesting in linear time', () => {
    const t = Date.now()
    const out = markdownForPaste('<ul><li>a'.repeat(25) + '</li></ul>'.repeat(25), 'a')
    expect(out).toContain('- a')
    expect(Date.now() - t).toBeLessThan(1000)
  })

  it('keeps tags in the text as text', () => {
    const out = md('<p>&lt;script&gt;alert(1)&lt;/script&gt; &lt;img src=x onerror=1&gt; &amp;amp; &lt;http://x&gt;</p>')
    expect(out).toBe('\\<script> alert(1)\\</script> \\<img src=x onerror=1> \\&amp; \\<http://x>'.replace(/> alert/, '>alert'))
    expect(out).not.toMatch(/(^|[^\\])</)
  })

  it('escapes list numbers properly and every line start', () => {
    expect(md('<p>1. x</p>')).toBe('1\\. x')
    expect(md('<p>a<br># h<br>- x</p>')).toBe('a  \n\\# h  \n\\- x')
  })
})

describe('inside an open code fence', () => {
  it('is odd fence lines before the caret', () => {
    expect(insideOpenFence('')).toBe(false)
    expect(insideOpenFence('hello\n')).toBe(false)
    expect(insideOpenFence('```go\n')).toBe(true)
    expect(insideOpenFence('```\ncode\n```\n')).toBe(false)
    expect(insideOpenFence('```\ncode\n```\n\n```py\n')).toBe(true)
  })

  it('counts ~~~ fences and fences indented up to three spaces', () => {
    expect(insideOpenFence('~~~\n')).toBe(true)
    expect(insideOpenFence('~~~sh\nx\n~~~')).toBe(false)
    expect(insideOpenFence('   ```\nx')).toBe(true)
    expect(insideOpenFence('    ```\nx')).toBe(false)
  })

  it('a different mark or a shorter fence does not close, and a longer one nests the shorter', () => {
    expect(insideOpenFence('```\n~~~\n')).toBe(true)
    expect(insideOpenFence('````md\n```\ninner\n```\n')).toBe(true)
    expect(insideOpenFence('````md\n```\ninner\n```\n````')).toBe(false)
    expect(insideOpenFence('```\n``` not a close\n')).toBe(true)
  })

  it('names the language of a GitHub block from its wrapper, a few levels up', () => {
    expect(md('<div class="snippet-clipboard-content"><div class="highlight highlight-source-go notranslate"><pre><code>x := 1</code></pre></div></div>')).toBe('```go\nx := 1\n```')
    expect(md('<pre data-language="rust"><code>fn a() {}</code></pre>')).toBe('```rust\nfn a() {}\n```')
  })
})
