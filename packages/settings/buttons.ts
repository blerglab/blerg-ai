import type * as TypeScript from 'typescript'

/**
 * The button guard (#229): every clickable control in the web apps — `<button>`, `<input type="button|submit|reset">`
 * and any element with `role="button"` — must be styled, never left with the browser's default look.
 *
 * Checked statically (no rendering) and closed by default: a control passes only when, for every value its
 * props can take, it provably gets a real background and a real border (or `all: unset|initial`) from
 * - CSS rules that provably match it at rest: outside any `@media`/`@supports`/… block, without pseudo-classes,
 *   attribute or id selectors, every class of the subject on the control, and every ancestor part of the
 *   selector matched by an element around it in the same file (`>` only across a direct DOM parent);
 * - its inline `style`, read in order (later properties and spreads win), with values that are known and set
 *   (`undefined`, `''`, `initial`, `unset`, `revert` and unreadable values do not count).
 * Props are read left to right like React does: a spread that cannot be resolved in the same file may
 * override anything before it, so the class is then unknown and the control fails. A reviewed exception is
 * a comment alone on the line above: `// button-guard-ignore: <reason>` or `{/* button-guard-ignore: <reason> *\/}`.
 */

/** The shared button classes, in the order the failure message lists them. */
export const DESIGN_SYSTEM_BUTTONS = [
  'btn', // runner, core and board: the app button (`primary`, `danger`, `small`, `ghost` modifiers)
  'icon-btn', // runner: a square glyph button
  'settings-action', // packages/settings: settings and dialog actions (`primary` modifier)
  'settings-toggle-control', // packages/settings: an immediate-save switch
  'artifact-btn', // packages/chat: chat, viewer and review actions
  'link-btn', // board: a text link that acts
] as const

export const OPT_OUT = 'button-guard-ignore:'

interface Compound { tag?: string, classes: string[] }
interface Rule {
  /** Compounds from the outermost ancestor to the subject. */
  compounds: Compound[]
  /** Combinators between compounds: ' ' (descendant) or '>' (child). */
  combinators: string[]
  declarations: [string, string][]
}
export interface ButtonStyles { rules: Rule[] }

const BACKGROUND = new Set(['background', 'background-color'])
const BORDER = /^border(-(top|right|bottom|left))?(-(style|width))?$/
const CSS_WIDE = /^(initial|unset|revert|revert-layer)$/i

/** A declared value that sets the property (not a reset to the browser's default, not empty). */
const setsValue = (value: string) => {
  const known = value.split(UNKNOWN).join('').replace(/!important/i, '').trim()
  return known !== '' && !CSS_WIDE.test(known)
}
const UNKNOWN = '\u0000'

/**
 * Both groups, background and border, must be set by valid declarations and by no invalid one;
 * `all: unset|initial` covers both.
 */
function replacesNativeLook(declarations: [string, string][]): boolean {
  const all = declarations.filter(([p]) => p === 'all')
  // `all: revert` (or anything unknown) can bring the browser's look back: fail closed.
  if (all.some(([, v]) => !/^(unset|initial)$/i.test(v.trim()))) return false
  if (all.length) return true
  const group = (match: (p: string) => boolean) => {
    const ds = declarations.filter(([p]) => match(p))
    return ds.length > 0 && ds.every(([, v]) => setsValue(v))
  }
  return group(p => BACKGROUND.has(p)) && group(p => BORDER.test(p))
}

function splitTopLevel(text: string, sep: string): string[] {
  const out: string[] = []
  let depth = 0, start = 0
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (c === '(' || c === '[') depth++
    else if (c === ')' || c === ']') depth--
    else if (c === sep && depth === 0) { out.push(text.slice(start, i)); start = i + 1 }
  }
  out.push(text.slice(start))
  return out
}

/** A selector the guard can prove, or null: pseudo-classes, pseudo-elements, attributes, ids and sibling combinators are not. */
function parseSelector(selector: string): Pick<Rule, 'compounds' | 'combinators'> | null {
  const parts = selector.trim().split(/\s*(>)\s*|\s+/).filter(p => p !== undefined && p !== '')
  const compounds: Compound[] = []
  const combinators: string[] = []
  let pending = ' '
  for (const part of parts) {
    if (part === '>') { pending = '>'; continue }
    const m = /^(\*|[a-zA-Z][\w-]*)?((?:\.-?[_a-zA-Z][\w-]*)*)$/.exec(part)
    if (!m) return null
    if (compounds.length) combinators.push(pending)
    pending = ' '
    compounds.push({ tag: m[1] && m[1] !== '*' ? m[1].toLowerCase() : undefined, classes: m[2].split('.').filter(Boolean) })
  }
  return compounds.length ? { compounds, combinators } : null
}

/**
 * Read the CSS an app loads into rules the guard can prove. Rules inside an at-rule block (other than
 * `@layer`) are conditional and left out; so are selectors `parseSelector` cannot prove.
 */
export function buttonStyles(css: Record<string, string>): ButtonStyles {
  const rules: Rule[] = []
  for (const text of Object.values(css)) {
    const clean = text.replace(/\/\*[\s\S]*?\*\//g, '')
    const conditional: boolean[] = []
    let buffer = ''
    for (let i = 0; i < clean.length; i++) {
      const c = clean[i]
      if (c === '{') {
        const prelude = buffer.trim()
        buffer = ''
        if (prelude.startsWith('@')) { conditional.push(!/^@layer\b/i.test(prelude)); continue }
        // A style rule: read its body; a nested block makes the whole rule conditional.
        let depth = 0, body = '', nested = false
        for (i++; i < clean.length; i++) {
          if (clean[i] === '{') { depth++; nested = true }
          else if (clean[i] === '}') { if (depth === 0) break; depth-- }
          body += clean[i]
        }
        if (nested || conditional.some(Boolean)) continue
        const declarations = body.split(';').map(d => {
          const at = d.indexOf(':')
          return [d.slice(0, at).trim().toLowerCase(), d.slice(at + 1).trim()] as [string, string]
        }).filter(([p]) => p)
        for (const selector of splitTopLevel(prelude, ',')) {
          const parsed = parseSelector(selector)
          if (parsed) rules.push({ ...parsed, declarations })
        }
      } else if (c === '}') { conditional.pop(); buffer = '' }
      else if (c === ';' && buffer.trim().startsWith('@')) buffer = ''
      else buffer += c
    }
  }
  return { rules }
}

/** An element in the DOM around a control, as far as the same file shows it. */
export interface Ancestor {
  tag: string
  /** Classes the element always has. */
  classes: Set<string>
  /** A component or a function call sits between this element and the one below it: it may not be the direct parent. */
  gap: boolean
}

const compoundMatches = (c: Compound, tag: string, classes: Set<string>) => (!c.tag || c.tag === tag) && c.classes.every(x => classes.has(x))

/** Does the rule provably match an element with this tag and these classes, inside these ancestors (nearest first)? */
export function ruleMatches(rule: Rule, tag: string, classes: Set<string>, ancestors: Ancestor[]): boolean {
  const n = rule.compounds.length
  if (!compoundMatches(rule.compounds[n - 1], tag, classes)) return false
  // Match compound k against ancestors from index a; `child` when it must be the direct parent.
  const from = (k: number, a: number, child: boolean): boolean => {
    if (k < 0) return true
    if (child) {
      const anc = ancestors[a]
      return !!anc && !anc.gap && compoundMatches(rule.compounds[k], anc.tag, anc.classes) && from(k - 1, a + 1, rule.combinators[k - 1] === '>')
    }
    for (let j = a; j < ancestors.length; j++) {
      if (compoundMatches(rule.compounds[k], ancestors[j].tag, ancestors[j].classes) && from(k - 1, j + 1, rule.combinators[k - 1] === '>')) return true
    }
    return false
  }
  return from(n - 2, 0, rule.combinators[n - 2] === '>')
}

const MAX_ALTERNATIVES = 64
type StyleMap = Map<string, string[]>

/** Check one file. `styles` comes from `buttonStyles` over the CSS the app loads. */
export function unstyledButtons(ts: typeof TypeScript, source: string, path: string, styles: ButtonStyles): string[] {
  const file = ts.createSourceFile(path, source, ts.ScriptTarget.Latest, true, path.endsWith('.tsx') ? ts.ScriptKind.TSX : ts.ScriptKind.TS)
  const scopeOf = (node: TypeScript.Node) => {
    let p: TypeScript.Node | undefined = node.parent
    while (p && !ts.isBlock(p) && !ts.isSourceFile(p) && !ts.isFunctionLike(p)) p = p.parent
    return p ?? file
  }
  const lines = source.split('\n')
  const failures: string[] = []
  const designSystem = (c: string) => replacesNativeLook(styles.rules.filter(r => ruleMatches(r, 'button', new Set([c]), [])).flatMap(r => r.declarations))
  const listed = DESIGN_SYSTEM_BUTTONS.filter(designSystem)

  // Same-file declarations, so `className={chip}`, `style={button}` and `{...props}` resolve to their values.
  // A const's initializer or a function declaration is a value; any other binding of the name (a parameter,
  // a `let`, a destructured name) is recorded too, so that it shadows outer consts and reads as unknown.
  const declarations = new Map<string, { node: TypeScript.Node, scope: TypeScript.Node, value: boolean }[]>()
  const declare = (name: string, node: TypeScript.Node, scope: TypeScript.Node, value: boolean) => declarations.set(name, [...(declarations.get(name) ?? []), { node, scope, value }])
  const bindingNames = (name: TypeScript.BindingName): string[] => ts.isIdentifier(name) ? [name.text]
    : name.elements.flatMap(el => ts.isOmittedExpression(el) ? [] : bindingNames(el.name))
  ;(function collect(node: TypeScript.Node) {
    if (ts.isVariableDeclaration(node)) {
      const constant = ts.isIdentifier(node.name) && !!node.initializer && !!(node.parent.flags & ts.NodeFlags.Const)
      for (const name of bindingNames(node.name)) declare(name, constant ? node.initializer! : node, scopeOf(node), constant)
    }
    if (ts.isParameter(node)) for (const name of bindingNames(node.name)) declare(name, node, node.parent, false)
    if (ts.isFunctionDeclaration(node) && node.name) declare(node.name.text, node, scopeOf(node), true)
    ts.forEachChild(node, collect)
  })(file)
  const contains = (outer: TypeScript.Node, inner: TypeScript.Node) => outer.pos <= inner.pos && inner.end <= outer.end
  /** The value of the innermost binding of this name around the use, if it is a const or a function. */
  const resolve = (id: TypeScript.Identifier): TypeScript.Node | undefined => {
    const found = (declarations.get(id.text) ?? []).filter(d => contains(d.scope, id) && d.node !== id && !contains(d.node, id))
      .sort((a, b) => (a.scope.end - a.scope.pos) - (b.scope.end - b.scope.pos))[0]
    return found?.value ? found.node : undefined
  }
  const unwrap = (e: TypeScript.Node): TypeScript.Node => {
    while (ts.isParenthesizedExpression(e) || ts.isAsExpression(e) || ts.isNonNullExpression(e) || ts.isSatisfiesExpression(e) || ts.isJsxExpression(e) || ts.isTypeAssertionExpression(e)) {
      const inner = (e as { expression?: TypeScript.Node }).expression
      if (!inner) break
      e = inner
    }
    return e
  }
  const product = (parts: string[][]): string[] => {
    let out = ['']
    for (const part of parts) {
      out = [...new Set(out.flatMap(a => part.map(b => a + b)))]
      if (out.length > MAX_ALTERNATIVES) return [UNKNOWN]
    }
    return out
  }
  /** The expressions a same-file function returns, or null. */
  function returned(callee: TypeScript.Node, depth: number): TypeScript.Node[] | null {
    let fn = ts.isIdentifier(callee) ? resolve(callee) : undefined
    if (fn && !ts.isFunctionDeclaration(fn)) fn = unwrap(fn)
    if (!fn || !(ts.isArrowFunction(fn) || ts.isFunctionExpression(fn) || ts.isFunctionDeclaration(fn)) || !fn.body || depth > 8) return null
    if (!ts.isBlock(fn.body)) return [fn.body]
    const out: TypeScript.Node[] = []
    ;(function find(n: TypeScript.Node) {
      if (ts.isReturnStatement(n) && n.expression) out.push(n.expression)
      if (!ts.isFunctionLike(n)) ts.forEachChild(n, find)
    })(fn.body)
    return out.length ? out : null
  }

  /** Every string the expression can produce; UNKNOWN marks a part that cannot be read statically. */
  function strings(node: TypeScript.Node | undefined, depth = 0): string[] {
    if (!node || depth > 8) return [UNKNOWN]
    const e = unwrap(node)
    if (ts.isStringLiteral(e) || ts.isNoSubstitutionTemplateLiteral(e) || ts.isNumericLiteral(e)) return [e.text]
    if (e.kind === ts.SyntaxKind.FalseKeyword || e.kind === ts.SyntaxKind.NullKeyword || (ts.isIdentifier(e) && e.text === 'undefined')) return ['']
    if (ts.isTemplateExpression(e)) return product([[e.head.text], ...e.templateSpans.flatMap(s => [strings(s.expression, depth + 1), [s.literal.text]])])
    if (ts.isConditionalExpression(e)) return [...new Set([...strings(e.whenTrue, depth + 1), ...strings(e.whenFalse, depth + 1)])]
    if (ts.isBinaryExpression(e)) {
      const op = e.operatorToken.kind
      if (op === ts.SyntaxKind.PlusToken) return product([strings(e.left, depth + 1), strings(e.right, depth + 1)])
      if (op === ts.SyntaxKind.AmpersandAmpersandToken) return ['', ...strings(e.right, depth + 1)]
      if (op === ts.SyntaxKind.BarBarToken || op === ts.SyntaxKind.QuestionQuestionToken) return [...new Set([...strings(e.left, depth + 1), ...strings(e.right, depth + 1)])]
    }
    if (ts.isIdentifier(e)) {
      const d = resolve(e)
      return d && !ts.isFunctionDeclaration(d) ? strings(d, depth + 1) : [UNKNOWN]
    }
    // [a, cond && b].filter(Boolean).join(' ')
    if (ts.isCallExpression(e) && ts.isPropertyAccessExpression(e.expression) && e.expression.name.text === 'join') {
      let target = unwrap(e.expression.expression)
      if (ts.isCallExpression(target) && ts.isPropertyAccessExpression(target.expression) && target.expression.name.text === 'filter') target = unwrap(target.expression.expression)
      const sep = e.arguments[0] && ts.isStringLiteral(e.arguments[0]) ? e.arguments[0].text : ','
      if (ts.isArrayLiteralExpression(target)) {
        const parts: string[][] = []
        target.elements.forEach((el, i) => { if (i) parts.push([sep]); parts.push(strings(el, depth + 1)) })
        return product(parts)
      }
    }
    return [UNKNOWN]
  }
  const tokens = (value: string) => value.split(/\s+/).filter(t => t && !t.includes(UNKNOWN))

  /** The object literals an expression can be (same-file consts, members and function returns), or null when one is unknown. */
  function objects(node: TypeScript.Node | undefined, depth = 0): (TypeScript.ObjectLiteralExpression | 'empty')[] | null {
    if (!node || depth > 8) return null
    const e = unwrap(node)
    if (ts.isObjectLiteralExpression(e)) return [e]
    if (e.kind === ts.SyntaxKind.NullKeyword || e.kind === ts.SyntaxKind.FalseKeyword || (ts.isIdentifier(e) && e.text === 'undefined')) return ['empty']
    if (ts.isIdentifier(e)) {
      const d = resolve(e)
      return d && !ts.isFunctionDeclaration(d) ? objects(d, depth + 1) : null
    }
    if (ts.isPropertyAccessExpression(e)) {
      const owners = objects(e.expression, depth + 1)
      if (!owners) return null
      const out: (TypeScript.ObjectLiteralExpression | 'empty')[] = []
      for (const owner of owners) {
        const prop = owner === 'empty' ? undefined : [...owner.properties].reverse().find(p => p.name && ts.isIdentifier(p.name) && p.name.text === e.name.text)
        if (!prop || !ts.isPropertyAssignment(prop)) return null
        const inner = objects(prop.initializer, depth + 1)
        if (!inner) return null
        out.push(...inner)
      }
      return out
    }
    if (ts.isConditionalExpression(e)) {
      const a = objects(e.whenTrue, depth + 1), b = objects(e.whenFalse, depth + 1)
      return a && b ? [...a, ...b] : null
    }
    if (ts.isBinaryExpression(e) && e.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken) {
      const b = objects(e.right, depth + 1)
      return b ? ['empty', ...b] : null
    }
    if (ts.isCallExpression(e)) {
      const rets = returned(e.expression, depth)
      if (!rets) return null
      const out: (TypeScript.ObjectLiteralExpression | 'empty')[] = []
      for (const r of rets) {
        const o = objects(r, depth + 1)
        if (!o) return null
        out.push(...o)
      }
      return out
    }
    return null
  }

  /**
   * An object's properties in order, as alternatives: name → its value expression. `null` in the list means
   * an alternative with a spread that cannot be read (it may set anything); later properties still win.
   */
  type Props = { known: Map<string, TypeScript.Node | 'true'>, open: boolean }
  function propsOf(entries: readonly (TypeScript.ObjectLiteralElementLike | TypeScript.JsxAttributeLike)[], depth: number): Props[] {
    let alts: Props[] = [{ known: new Map(), open: false }]
    for (const entry of entries) {
      if (ts.isJsxAttribute(entry)) {
        const name = entry.name.getText(file)
        alts.forEach(a => a.known.set(name, entry.initializer ?? 'true'))
      } else if (ts.isPropertyAssignment(entry) && entry.name && (ts.isIdentifier(entry.name) || ts.isStringLiteral(entry.name))) {
        const name = entry.name.text
        alts.forEach(a => a.known.set(name, entry.initializer))
      } else if (ts.isShorthandPropertyAssignment(entry)) {
        alts.forEach(a => a.known.set(entry.name.text, entry.name))
      } else if (ts.isSpreadAssignment(entry) || ts.isJsxSpreadAttribute(entry)) {
        const objs = depth > 8 ? null : objects(entry.expression)
        if (!objs) { alts = alts.map(() => ({ known: new Map(), open: true })); continue }
        const next: Props[] = []
        for (const a of alts) for (const o of objs) {
          if (o === 'empty') { next.push({ known: new Map(a.known), open: a.open }); continue }
          for (const inner of propsOf(o.properties, depth + 1)) {
            next.push(inner.open ? { known: new Map(inner.known), open: true } : { known: new Map([...a.known, ...inner.known]), open: a.open })
          }
        }
        alts = next.slice(0, MAX_ALTERNATIVES)
      } else {
        alts = alts.map(() => ({ known: new Map(), open: true }))
      }
    }
    return alts
  }
  /** A prop's possible values: `undefined` when absent, UNKNOWN when an unreadable spread may set it. */
  const valuesOf = (props: Props, name: string): string[] | undefined => {
    const v = props.known.get(name)
    if (v === undefined) return props.open ? [UNKNOWN] : undefined
    return v === 'true' ? ['true'] : strings(v)
  }

  /** Inline style alternatives: kebab-case property → possible values; null when unreadable. */
  function styleMaps(node: TypeScript.Node | 'true' | undefined): (StyleMap | null)[] {
    if (node === undefined) return [new Map()]
    if (node === 'true') return [null]
    const objs = objects(node)
    if (!objs) return [null]
    const out: (StyleMap | null)[] = []
    for (const o of objs) {
      if (o === 'empty') { out.push(new Map()); continue }
      for (const p of propsOf(o.properties, 0)) {
        if (p.open) { out.push(null); continue }
        const map: StyleMap = new Map()
        for (const [name, value] of p.known) map.set(name.replace(/[A-Z]/g, c => '-' + c.toLowerCase()).replace(/^(webkit|moz|ms)-/, '-$1-'), value === 'true' ? [UNKNOWN] : strings(value))
        out.push(map)
      }
    }
    return out
  }

  /** The DOM elements around a node in this file, nearest first, as far as they can be read. */
  function ancestorsOf(node: TypeScript.Node): Ancestor[] {
    const out: Ancestor[] = []
    let gap = false
    let top = node
    for (let p = node.parent; p; top = p, p = p.parent) {
      // Through an attribute (a render prop, an icon prop) or out of a function the element is placed elsewhere: stop.
      // A callback passed to a call (`items.map(i => …)`) renders in place.
      if (ts.isJsxAttribute(p) || ts.isJsxSpreadAttribute(p) || ts.isSourceFile(p)) break
      if (ts.isFunctionLike(p)) {
        if (ts.isArrowFunction(p) || ts.isFunctionExpression(p)) { if (p.parent && ts.isCallExpression(p.parent) && p.parent.arguments.includes(p)) continue }
        break
      }
      // An element handed to a function (`withHover(…, <button/>)`) may be wrapped.
      if (ts.isCallExpression(p) && p.arguments.includes(top as TypeScript.Expression) && !ts.isArrowFunction(top) && !ts.isFunctionExpression(top)) { gap = true; continue }
      if (!ts.isJsxElement(p) || p.openingElement === top) continue
      const tag = p.openingElement.tagName.getText(file)
      if (!/^[a-z]/.test(tag)) { gap = true; continue }
      const values = propsOf(p.openingElement.attributes.properties, 0).flatMap(a => valuesOf(a, 'className') ?? [''])
      const classes = values.map(v => new Set(tokens(v))).reduce((a, b) => new Set([...a].filter(x => b.has(x))))
      out.push({ tag, classes, gap })
      gap = false
    }
    return out
  }

  // Real comments only: trivia the parser keeps, never JSX text, strings or attribute values.
  const comments: { text: string, line: number }[] = []
  ;(function scan(node: TypeScript.Node) {
    if (ts.isJsxText(node)) return
    // Before a JSX child, an opening or a closing tag lies JSX text, not trivia; inside `{…}` lie real comments.
    const inText = ts.isJsxOpeningElement(node) || ts.isJsxClosingElement(node) || ts.isJsxOpeningFragment(node) || ts.isJsxClosingFragment(node)
      || (!!node.parent && (ts.isJsxElement(node.parent) || ts.isJsxFragment(node.parent)))
    const at = ts.isJsxExpression(node) ? node.getStart(file) + 1 : inText ? -1 : node.pos
    // Leading ranges start after a line break; trailing ones cover the rest of the first line (`{/* … */}`).
    if (at >= 0) for (const r of [...ts.getTrailingCommentRanges(source, at) ?? [], ...ts.getLeadingCommentRanges(source, at) ?? []]) comments.push({ text: source.slice(r.pos, r.end), line: file.getLineAndCharacterOfPosition(r.end).line })
    ts.forEachChild(node, scan)
  })(file)
  const OPT_OUT_COMMENT = /^(?:\/\/\s*button-guard-ignore:[ \t]*(?<line>.*?)\s*|\/\*\s*button-guard-ignore:[ \t]*(?<block>.*?)\s*\*\/)$/
  function optedOut(node: TypeScript.Node): boolean {
    const above = file.getLineAndCharacterOfPosition(node.getStart(file)).line - 1
    if (above < 0) return false
    const line = lines[above].trim()
    return comments.some(c => {
      if (c.line !== above) return false
      const m = OPT_OUT_COMMENT.exec(c.text)
      const reason = m && (m.groups?.line ?? m.groups?.block ?? '')
      // The comment is the whole line (`{…}` around it in JSX) and gives a reason.
      return !!reason && /\w/.test(reason) && (line === c.text || line === `{${c.text}}`)
    })
  }

  const isControl = (tag: string, props: Props) => {
    if (!/^[a-z]/.test(tag)) return false
    if (tag === 'button') return true
    const role = valuesOf(props, 'role')
    // An unreadable spread on an anchor or input may make it a button; on other elements the guard cannot tell.
    if (role?.some(r => r === 'button' || (r.includes(UNKNOWN) && (tag === 'a' || tag === 'input')))) return true
    if (tag !== 'input') return false
    const type = valuesOf(props, 'type')
    return !!type?.some(t => /^(button|submit|reset)$/.test(t) || t.includes(UNKNOWN))
  }

  function visit(node: TypeScript.Node) {
    if (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) {
      const tag = node.tagName.getText(file)
      const alts = propsOf(node.attributes.properties, 0)
      const controlAlts = alts.filter(a => isControl(tag, a))
      if (controlAlts.length && !optedOut(node)) {
        const ancestors = ancestorsOf(node)
        let reason = ''
        for (const props of controlAlts) {
          const classValues = valuesOf(props, 'className') ?? ['']
          const styleNode = props.known.get('style') ?? (props.open ? 'true' : undefined)
          const styleAlts = styleMaps(styleNode)
          for (const value of classValues) {
            if (value === UNKNOWN) { reason ||= 'has a class that cannot be read statically (an unresolved spread or value may set it)'; continue }
            const classes = new Set(tokens(value))
            const css = styles.rules.filter(r => ruleMatches(r, tag, classes, ancestors)).flatMap(r => r.declarations)
            for (const style of styleAlts) {
              // An unreadable style object adds nothing the guard can count on; the CSS must do it alone.
              const inline = style ?? new Map<string, string[]>()
              // Inline properties win over CSS, group by group; an unset or unreadable inline value fails.
              const inlineDecls = [...inline].flatMap(([p, vs]) => vs.map(v => [p, v] as [string, string]))
              const touched = (match: (p: string) => boolean) => inlineDecls.some(([p]) => match(p))
              const merged = [
                ...css.filter(([p]) => !(BACKGROUND.has(p) && touched(q => BACKGROUND.has(q))) && !(BORDER.test(p) && touched(q => BORDER.test(q))) && !(p === 'all' && touched(q => q === 'all'))),
                ...inlineDecls,
              ]
              if (replacesNativeLook(merged)) continue
              reason ||= inlineDecls.some(([p, v]) => (BACKGROUND.has(p) || BORDER.test(p) || p === 'all') && !setsValue(v))
                ? 'has an inline background or border that is unset or cannot be read statically'
                : !style ? 'has an inline style that cannot be read statically, and its class alone does not style it'
                  : classes.size ? `has no approved button class (found "${[...classes].join(' ')}")` : 'has no class'
            }
          }
        }
        if (reason) {
          const line = file.getLineAndCharacterOfPosition(node.getStart(file)).line + 1
          const what = tag === 'button' ? '<button>' : tag === 'input' ? '<input type="button|submit|reset">' : `<${tag} role="button">`
          failures.push(`${path}:${line}: ${what} ${reason}; ` +
            `use one of ${listed.join(', ')} (or a component class this app's CSS styles as a button), ` +
            `or mark a reviewed exception with \`// ${OPT_OUT} <reason>\` alone on the line above.`)
        }
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(file)
  return failures
}
