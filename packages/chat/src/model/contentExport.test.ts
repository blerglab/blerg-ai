import { expect, it } from 'vitest'
import { codeFilename, printDocument, registerPrintVisual, tableCsv, tableMarkdown } from './contentExport'

function table() {
  const root = document.createElement('div')
  root.innerHTML = '<table><thead><tr><th>Name</th><th>Value</th></tr></thead><tbody><tr><td><b>Total</b>, "net"</td><td>=1+2</td></tr><tr><td>+sum<br>next</td><td> @cmd</td></tr><tr><td>-2</td><td>0</td></tr></tbody></table>'
  return root.querySelector('table')!
}
it('quotes shown cell text, headers, multiline cells and spreadsheet formulas', () => {
  expect(tableCsv(table())).toBe('"Name","Value"\r\n"Total, ""net""","\'=1+2"\r\n"\'+sum\nnext","\' @cmd"\r\n"\'-2","0"\r\n')
  expect(tableMarkdown(table())).toContain('| +sum<br>next |  @cmd |')
  expect(tableMarkdown(table())).toContain('| --- | --- |')
})
it.each([['typescript', 'code.ts'], ['python', 'code.py'], ['go', 'code.go'], [null, 'code.txt'], ['dockerfile', 'Dockerfile']])('names code files for %s', (lang, name) => {
  expect(codeFilename(lang)).toBe(name)
})
it('prints content without controls, active markup, app chrome or clipped table styles', () => {
  const root = document.createElement('div')
  root.innerHTML = '<div role="toolbar"><button>Download</button></div><div style="max-height:50px;overflow:hidden"><table><thead><tr><th>Title</th></tr></thead><tbody><tr><td>Shown</td></tr></tbody></table></div><script>bad()</script>'
  const html = printDocument(root)
  const parsed = new DOMParser().parseFromString(html, 'text/html')
  expect(parsed.querySelector('button,script,[role=toolbar],[style]')).toBeNull()
  expect(parsed.body.textContent).toContain('Shown')
  expect(html).toContain('table-header-group')
  expect(html).toContain('break-inside: avoid')
  expect(html).toContain('color-scheme: light')
})
it('prints the current renderer snapshot instead of a sandboxed drawing frame', () => {
  const root = document.createElement('div')
  root.innerHTML = '<div class="visual-surface"><iframe></iframe></div>'
  const unregister = registerPrintVisual(root.firstElementChild as HTMLElement, () => '<svg xmlns="http://www.w3.org/2000/svg"><text>Current chart</text></svg>')
  const printed = new DOMParser().parseFromString(printDocument(root), 'text/html')
  expect(printed.querySelector('iframe')).toBeNull()
  expect(printed.querySelector('svg')?.textContent).toBe('Current chart')
  unregister()
})

it('prints sanitized static HTML with an honest limitation note', () => {
  const root = document.createElement('div')
  const frame = document.createElement('iframe')
  frame.srcdoc = '<h1>Static report</h1><script>document.body.textContent="Live"</script>'
  root.append(frame)
  const printed = new DOMParser().parseFromString(printDocument(root), 'text/html')
  expect(printed.body.textContent).toContain('Static report')
  expect(printed.body.textContent).toContain('Scripts and live changes are not included.')
  expect(printed.querySelector('script,iframe')).toBeNull()
})
it('preserves sanitized SVG styles and IDs used by diagram geometry', () => {
  const root = document.createElement('div')
  root.innerHTML = '<div class="visual-surface"></div>'
  registerPrintVisual(root.firstElementChild as HTMLElement, () => '<svg xmlns="http://www.w3.org/2000/svg"><style>#diagram .node{fill:red}</style><g id="diagram"><rect class="node" width="20" height="20"/></g></svg>')
  const printed = new DOMParser().parseFromString(printDocument(root), 'text/html')
  expect(printed.querySelector('svg style')?.textContent).toContain('#diagram .node')
  expect(printed.querySelector('svg #diagram .node')).not.toBeNull()
})
