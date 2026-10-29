import type * as TypeScript from 'typescript'

/** Scan JSX so error/loading/conditional controls cannot escape a rendered happy-path check. */
export function unstyledSettings(ts: typeof TypeScript, source: string, path: string): string[] {
  const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true, path.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
  const failures: string[] = []
  function visit(node: TypeScript.Node) {
    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      const tag = node.tagName.getText(file)
      const attrs = node.attributes.properties
      const type = attrs.find(a => ts.isJsxAttribute(a) && a.name.getText(file) === 'type')
      if (tag === 'button' || tag === 'select' || (tag === 'input' && /["'](?:checkbox|radio)["']/.test(type?.getText(file) ?? ''))) {
        const attr = attrs.find(a => ts.isJsxAttribute(a) && a.name.getText(file) === 'className')
        const initial = attr && ts.isJsxAttribute(attr) ? attr.initializer : undefined
        const expr = initial && ts.isJsxExpression(initial) ? initial.expression : undefined
        // Require an unconditional class, including a literal prefix before dynamic app classes.
        const cls = initial && ts.isStringLiteral(initial) ? initial.text
          : expr && ts.isBinaryExpression(expr) && expr.operatorToken.kind === ts.SyntaxKind.PlusToken && ts.isStringLiteral(expr.left) ? expr.left.text
          : expr && ts.isTemplateExpression(expr) ? expr.head.text : ''
        const expected = tag === 'button' ? /settings-(action|toggle-control)/ : tag === 'select' ? /settings-field/ : /settings-toggle-control/
        if (!expected.test(cls)) {
          const line = file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1
          failures.push(`${path}:${line}: bare <${tag}> in settings; use the shared settings primitive (${expected.source}).`)
        }
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(file)
  return failures
}

/** Follow local component imports so a newly added settings section is guarded automatically. */
export function settingsScreens(ts: typeof TypeScript, sources: Record<string, string>, roots: readonly string[], containers: readonly string[] = []): Record<string, string> {
  for (const root of roots) if (!(root in sources)) throw new Error(`Settings guard cannot find ${root}; update the settings roots.`)
  const found: Record<string, string> = {}
  const queue = [...roots]
  for (const path of queue) {
    if (path in found || containers.includes(path) || !(path in sources)) continue
    found[path] = sources[path]
    const file = ts.createSourceFile(path, sources[path], ts.ScriptTarget.Latest, true, path.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
    for (const node of file.statements) {
      if (!ts.isImportDeclaration(node) || !ts.isStringLiteral(node.moduleSpecifier)) continue
      const specifier = node.moduleSpecifier.text
      const clause = node.importClause
      if (!specifier.startsWith('.') || !clause || clause.isTypeOnly) continue
      const names = [clause.name?.text]
      const bindings = clause.namedBindings
      if (bindings && ts.isNamedImports(bindings)) names.push(...bindings.elements.filter(e => !e.isTypeOnly).map(e => e.name.text))
      else if (bindings && ts.isNamespaceImport(bindings)) names.push(bindings.name.text)
      if (!names.some(n => n && /^[A-Z]/.test(n))) continue
      const parts: string[] = []
      for (const part of [...path.split('/').slice(0, -1), ...specifier.split('/')]) {
        if (part === '..') parts.pop()
        else if (part && part !== '.') parts.push(part)
      }
      const base = './' + parts.join('/')
      for (const candidate of [base, base + '.tsx', base + '/index.tsx', base + '.ts']) {
        if (candidate in sources) { queue.push(candidate); break }
      }
    }
  }
  return found
}
