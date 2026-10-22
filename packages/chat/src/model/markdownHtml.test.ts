import { describe, expect, it } from 'vitest'
import { classifyUrl, htmlSanitizeRuns, imageBlobType, placeholderTagForTests, resolveDocPath, sanitizeChildren, slugify, TOO_DEEP, UNCLOSED, type HNode } from './markdownHtml'

describe('classifyUrl', () => {
  it.each([
    ['https://example.com/a.png', 'src', 'remote'],
    ['http://example.com', 'href', 'remote'],
    ['docs/a.md', 'href', 'relative'],
    ['./a.md', 'href', 'relative'],
    ['../x/a.md', 'href', 'relative'],
    ['/root.md', 'href', 'relative'],
    ['LICENSE', 'href', 'relative'],
    ['#what-you-can-do', 'href', 'anchor'],
    ['data:image/png;base64,iVBORw0KGgo=', 'src', 'data-image'],
  ] as const)('%s (%s) is %s', (url, use, kind) => {
    expect(classifyUrl(url, use)?.kind).toBe(kind)
  })

  it.each([
    ['javascript:alert(1)', 'href'],
    [' JaVaScRiPt:alert(1)', 'href'],
    ['java\tscript:alert(1)', 'href'],
    ['java\nscript:alert(1)', 'href'],
    ['\u0001javascript:alert(1)', 'href'],
    ['vbscript:msgbox(1)', 'href'],
    ['data:text/html,<script>alert(1)</script>', 'href'],
    ['data:text/html;base64,PHNjcmlwdD4=', 'src'],
    ['data:image/svg+xml,<svg onload=alert(1)>', 'src'],
    ['data:image/svg+xml;base64,PHN2Zz4=', 'src'],
    ['mailto:a@b.c', 'href'],
    ['file:///etc/passwd', 'href'],
    ['//evil.example/x', 'href'],
    ['\\\\evil.example/x', 'href'],
    ['/\\evil.example/x', 'href'],
    ['#x', 'src'],
    ['https:foo', 'href'],
    ['https:foo/bar.png', 'src'],
    ['http:/one-slash', 'href'],
    ['', 'href'],
  ] as const)('refuses %j as %s', (url, use) => {
    expect(classifyUrl(url, use)).toBeNull()
  })
})

describe('resolveDocPath', () => {
  it.each([
    ['README.md', 'AGENTS.md', 'AGENTS.md'],
    ['README.md', 'install/desktop/README.md', 'install/desktop/README.md'],
    ['docs/guide/a.md', '../b.md', 'docs/b.md'],
    ['docs/a.md', './img/x.png', 'docs/img/x.png'],
    ['docs/a.md', '/brand/x.png', 'brand/x.png'],
    ['docs/a.md', 'b.md#L12', 'docs/b.md#L12'],
    [undefined, 'a%20b.md', 'a%20b.md'],
  ])('%s + %s = %s', (doc, href, want) => {
    expect(resolveDocPath(doc, href)).toBe(want)
  })
  it('refuses a path that climbs out of the folder', () => {
    expect(resolveDocPath('README.md', '../secret')).toBeNull()
    expect(resolveDocPath('docs/a.md', '../../x')).toBeNull()
  })
})

describe('slugify', () => {
  it('makes GitHub heading slugs', () => {
    expect(slugify('What you can do')).toBe('what-you-can-do')
    expect(slugify('Install (desktop) & run!')).toBe('install-desktop--run')
    expect(slugify('Ünïcode Wörks')).toBe('ünïcode-wörks')
  })
})

describe('sanitizeChildren', () => {
  const raw = (value: string): HNode => ({ type: 'raw', value })
  it('keeps allowlisted elements and attributes only', () => {
    const [p] = sanitizeChildren([raw('<p align="center" class="x" id="app" style="color:red" onclick="x()"><b>hi</b></p>')])
    expect(p.tagName).toBe('p')
    expect(p.properties).toEqual({ align: 'center' })
    expect(p.children?.[0].tagName).toBe('b')
  })
  it('puts Markdown nodes back where the HTML wrapped them, once each', () => {
    const md: HNode = { type: 'element', tagName: 'p', properties: {}, children: [{ type: 'text', value: 'inside' }] }
    const out = sanitizeChildren([raw('<details><summary>More</summary>'), { type: 'text', value: '\n' }, md, raw('</details>')])
    expect(out[0].tagName).toBe('details')
    expect(out[0].children?.some(c => c === md)).toBe(true)
  })
  it('drops a placeholder the author wrote, even one with this page’s tag name and a real index', () => {
    const tag = placeholderTagForTests()
    const md: HNode = { type: 'element', tagName: 'p', properties: {}, children: [{ type: 'text', value: 'moved?' }] }
    // Index 1 is a real Markdown element: honoring the forged one would pull it inside <b>.
    const out = sanitizeChildren([raw(`<b><${tag} data-blerg-slot="1"></${tag}></b>`), md])
    expect(out[0].tagName).toBe('b')
    expect(out[0].children).toEqual([])
    expect(out[1]).toBe(md)
  })
  it('lets a placeholder stand only for a Markdown-made element', () => {
    // A tag name split across two raw children escapes the rename (Markdown never splits one so, but
    // the second line of defense must hold anyway): index 1 is a raw child, never placed.
    const tag = placeholderTagForTests()
    const cut = 'blerg-md-'.length
    const out = sanitizeChildren([raw(`<i>x<${tag.slice(0, cut)}`), raw(`${tag.slice(cut)} data-blerg-slot="1"></i>`)])
    expect(out[0].tagName).toBe('i')
    expect(out[0].children).toEqual([{ type: 'text', value: 'x' }])
  })
  it.each(['math', 'svg', 'title', 'plaintext', 'xmp', 'noembed', 'iframe', 'select'])('keeps the Markdown after an unclosed <%s>, behind a note', tag => {
    const heading: HNode = { type: 'element', tagName: 'h2', properties: {}, children: [{ type: 'text', value: 'Later heading' }] }
    const list: HNode = { type: 'element', tagName: 'ul', properties: {}, children: [] }
    const out = sanitizeChildren([raw(`<${tag}>`), { type: 'text', value: '\n' }, heading, { type: 'text', value: '\n' }, list])
    const note = out.findIndex(n => n.properties?.className && (n.properties.className as string[]).includes('md-unclosed'))
    expect(note).toBeGreaterThanOrEqual(0)
    expect(out[note].children?.[0].value).toBe(UNCLOSED)
    expect(out.slice(note + 1).filter(n => n.type === 'element' && n.properties?.dataMdHidden === undefined)).toEqual([heading, list])
  })
  it.each([
    ['a comment', 'Read me <!-- and approve the rest -->', true],
    ['a script', '<script>steps</script>ok', true],
    ['a title', '<title>secret</title>ok', true],
    ['a template', '<template>x y</template>ok', true],
    ['an unknown element', '<center>shown</center>', false],
    ['plain allowed HTML', '<p align="center"><b>all shown</b></p>', false],
  ])('marks %s as hidden content: %s', (_name, html, hidden) => {
    const out = sanitizeChildren([raw(html)])
    expect(out.some(n => n.properties?.dataMdHidden === '')).toBe(hidden)
  })
  it('never keeps a source (it would load an image by itself)', () => {
    const out = sanitizeChildren([raw('<picture><source srcset="https://t.example/p.png"><img alt="a" src="https://t.example/a.png" srcset="https://t.example/b.png 2x"></picture>')])
    const picture = out[0]
    expect(picture.children?.map(c => c.tagName)).toEqual(['img'])
    expect(picture.children?.[0].properties).toEqual({ alt: 'a', src: 'https://t.example/a.png' })
  })

  it('clamps width and height', () => {
    const [img] = sanitizeChildren([raw('<img alt="a" width="99999" height="1234567">')])
    expect(img.properties).toMatchObject({ width: '4096', height: '4096' })
    const [td] = sanitizeChildren([raw('<table><tr><td width="250%">x</td></tr></table>')])[0].children![0].children![0].children!
    expect(td.properties).toMatchObject({ width: '100%' })
  })
  it('says so when nesting goes past 200 levels, instead of dropping the rest in silence', () => {
    const deep = '<div>'.repeat(260) + 'bottom words' + '</div>'.repeat(260)
    const text = (n: HNode): string => n.type === 'text' ? n.value ?? '' : (n.children ?? []).map(text).join('')
    const all = sanitizeChildren([raw(deep)]).map(text).join('')
    expect(all).toContain(TOO_DEEP)
    expect(all.split(TOO_DEEP)).toHaveLength(2)
  })
  it('sanitizes the same HTML once, and hands out fresh nodes each time', () => {
    const html = `<p align="center"><b>cached ${Math.random()}</b></p>`
    const before = htmlSanitizeRuns()
    const a = sanitizeChildren([raw(html)])
    const b = sanitizeChildren([raw(html)])
    expect(htmlSanitizeRuns()).toBe(before + 1)
    expect(a).toEqual(b)
    expect(a[0]).not.toBe(b[0])
    expect(a[0].properties).not.toBe(b[0].properties)
  })
})

describe('imageBlobType', () => {
  it('types images an <img> may draw, and nothing else', () => {
    expect(imageBlobType('image/svg+xml')).toBe('') // an SVG is never a typed blob: (mediaUrl)
    expect(imageBlobType('image/PNG; charset=binary')).toBe('image/png')
    expect(imageBlobType('text/html')).toBe('')
    expect(imageBlobType('image/svg+xml-evil')).toBe('')
  })
})
