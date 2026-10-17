// Code colour themes: the --chat-code-* and --chat-diff-* tokens as named sets, so an app can offer
// "GitHub Dark" or "Nord" for the syntax colours of every code block, viewer and preview without
// touching the rest of the chat's theme. Pure data and three small helpers; nothing here reads the
// network, and a value that is not a #rrggbb colour never reaches a stylesheet.

/** The code tokens, by key, and the custom property each one sets. */
export const CODE_TOKEN_NAMES = {
  fg: '--chat-code-fg',
  bg: '--chat-code-bg',
  linenum: '--chat-code-linenum',
  keyword: '--chat-code-keyword',
  string: '--chat-code-string',
  number: '--chat-code-number',
  comment: '--chat-code-comment',
  title: '--chat-code-title',
  type: '--chat-code-type',
  variable: '--chat-code-variable',
  builtin: '--chat-code-builtin',
  operator: '--chat-code-operator',
  diffAdd: '--chat-diff-add',
  diffDel: '--chat-diff-del',
} as const

export type CodeTokenKey = keyof typeof CODE_TOKEN_NAMES
export const CODE_TOKEN_KEYS = Object.keys(CODE_TOKEN_NAMES) as CodeTokenKey[]

/** What each token colours, for the editor's labels. */
export const CODE_TOKEN_LABELS: Record<CodeTokenKey, string> = {
  fg: 'Text',
  bg: 'Background',
  linenum: 'Line numbers',
  keyword: 'Keywords',
  string: 'Strings',
  number: 'Numbers',
  comment: 'Comments',
  title: 'Functions',
  type: 'Types',
  variable: 'Variables and attributes',
  builtin: 'Built-ins and meta',
  operator: 'Operators',
  diffAdd: 'Diff added',
  diffDel: 'Diff removed',
}

export type CodeTokens = Record<CodeTokenKey, string>
export type CodeMode = 'dark' | 'light'

export interface CodeTheme {
  id: string
  name: string
  /** The theme's colours; for a theme that has a light variant, the dark ones. */
  tokens: CodeTokens
  /** Only Blerg has one: it follows the app's palette, so light mode gets its own colours. */
  light?: CodeTokens
}

const t = (
  fg: string, bg: string, linenum: string, keyword: string, string: string, number: string, comment: string,
  title: string, type: string, variable: string, builtin: string, operator: string, diffAdd: string, diffDel: string,
): CodeTokens => ({ fg, bg, linenum, keyword, string, number, comment, title, type, variable, builtin, operator, diffAdd, diffDel })

const list: CodeTheme[] = [
  {
    id: 'blerg', name: 'Blerg',
    // Follows the app's palette (the var() references), so it matches the rest of the page.
    tokens: t('var(--chat-fg)', 'var(--chat-bg)', 'var(--chat-muted-2)', '#b69cf0', '#8fc9a0', '#e0b36a', '#7d8798',
      '#7fb2ee', '#6fc7c7', '#e0b36a', '#6fc7c7', 'var(--chat-muted)', 'var(--diff-add, #8ed6a6)', 'var(--diff-del, #ff9d94)'),
    light: t('var(--chat-fg)', 'var(--chat-bg)', 'var(--chat-muted-2)', '#7a52c7', '#2f7a4a', '#a15f0d', '#6b7684',
      '#2f62b8', '#1f7f7f', '#a15f0d', '#1f7f7f', 'var(--chat-muted)', 'var(--diff-add, #2f8a52)', 'var(--diff-del, #cf4b4b)'),
  },
  { id: 'github-dark', name: 'GitHub Dark', tokens: t('#c9d1d9', '#0d1117', '#6e7681', '#ff7b72', '#a5d6ff', '#79c0ff', '#8b949e', '#d2a8ff', '#ffa657', '#79c0ff', '#79c0ff', '#c9d1d9', '#7ee787', '#ffa198') },
  { id: 'github-light', name: 'GitHub Light', tokens: t('#24292f', '#ffffff', '#8c959f', '#cf222e', '#0a3069', '#0550ae', '#6e7781', '#8250df', '#953800', '#0550ae', '#0550ae', '#24292f', '#116329', '#82071e') },
  { id: 'one-dark', name: 'One Dark', tokens: t('#abb2bf', '#282c34', '#4b5263', '#c678dd', '#98c379', '#d19a66', '#5c6370', '#61afef', '#e5c07b', '#e06c75', '#56b6c2', '#56b6c2', '#98c379', '#e06c75') },
  { id: 'dracula', name: 'Dracula', tokens: t('#f8f8f2', '#282a36', '#6272a4', '#ff79c6', '#f1fa8c', '#bd93f9', '#6272a4', '#50fa7b', '#8be9fd', '#ffb86c', '#8be9fd', '#ff79c6', '#50fa7b', '#ff5555') },
  { id: 'monokai', name: 'Monokai', tokens: t('#f8f8f2', '#272822', '#90908a', '#f92672', '#e6db74', '#ae81ff', '#75715e', '#a6e22e', '#66d9ef', '#fd971f', '#66d9ef', '#f92672', '#a6e22e', '#f92672') },
  { id: 'solarized-dark', name: 'Solarized Dark', tokens: t('#839496', '#002b36', '#586e75', '#859900', '#2aa198', '#d33682', '#586e75', '#268bd2', '#b58900', '#cb4b16', '#268bd2', '#93a1a1', '#859900', '#dc322f') },
  { id: 'solarized-light', name: 'Solarized Light', tokens: t('#657b83', '#fdf6e3', '#93a1a1', '#859900', '#2aa198', '#d33682', '#93a1a1', '#268bd2', '#b58900', '#cb4b16', '#268bd2', '#586e75', '#859900', '#dc322f') },
  { id: 'nord', name: 'Nord', tokens: t('#d8dee9', '#2e3440', '#4c566a', '#81a1c1', '#a3be8c', '#b48ead', '#616e88', '#88c0d0', '#8fbcbb', '#ebcb8b', '#81a1c1', '#81a1c1', '#a3be8c', '#bf616a') },
  { id: 'gruvbox-dark', name: 'Gruvbox Dark', tokens: t('#ebdbb2', '#282828', '#7c6f64', '#fb4934', '#b8bb26', '#d3869b', '#928374', '#fabd2f', '#83a598', '#8ec07c', '#fe8019', '#a89984', '#b8bb26', '#fb4934') },
]

/** Every theme, in the order a picker lists them. */
export const CODE_THEMES: readonly CodeTheme[] = list
export const codeThemes: Readonly<Record<string, CodeTheme>> = Object.fromEntries(list.map(x => [x.id, x]))
export const CODE_THEME_IDS: readonly string[] = list.map(x => x.id)

/** A person's choice: a theme per mode and, on top of it, a colour per token. */
export interface CodeColours {
  dark: string
  light: string
  overrides: { dark: Partial<CodeTokens>; light: Partial<CodeTokens> }
}

export const DEFAULT_CODE_COLOURS: CodeColours = { dark: 'blerg', light: 'blerg', overrides: { dark: {}, light: {} } }

const HEX = /^#[0-9a-fA-F]{6}$/
export const isHexColour = (v: unknown): v is string => typeof v === 'string' && HEX.test(v)

/** Why `v` is not a valid choice, or '' when it is. Strict: unknown ids, keys and colours are refused. */
export function validateCodeColours(v: unknown): string {
  const obj = (x: unknown): x is Record<string, unknown> => typeof x === 'object' && x !== null && !Array.isArray(x)
  if (!obj(v)) return 'code_colours must be an object'
  for (const k of Object.keys(v)) if (!['dark', 'light', 'overrides'].includes(k)) return `unknown field ${k}`
  for (const m of ['dark', 'light'] as const) {
    if (typeof v[m] !== 'string' || !Object.hasOwn(codeThemes, v[m] as string)) return `unknown theme for ${m}`
  }
  const ov = v.overrides
  if (ov === undefined) return ''
  if (!obj(ov)) return 'overrides must be an object'
  for (const m of Object.keys(ov)) {
    if (m !== 'dark' && m !== 'light') return `unknown mode ${m}`
    const toks = ov[m]
    if (!obj(toks)) return `overrides.${m} must be an object`
    for (const [k, c] of Object.entries(toks)) {
      if (!(k in CODE_TOKEN_NAMES)) return `unknown token ${k}`
      if (!isHexColour(c)) return `${k} must be #rrggbb`
    }
  }
  return ''
}

/** A stored or received value made safe: anything invalid falls back to the default piece by piece. */
export function normaliseCodeColours(v: unknown): CodeColours {
  const out: CodeColours = { dark: 'blerg', light: 'blerg', overrides: { dark: {}, light: {} } }
  if (typeof v !== 'object' || v === null) return out
  const r = v as Record<string, unknown>
  for (const m of ['dark', 'light'] as const) {
    const id = r[m]
    if (typeof id === 'string' && Object.hasOwn(codeThemes, id)) out[m] = id
    const ov = (r.overrides as Record<string, unknown> | undefined)?.[m]
    if (typeof ov === 'object' && ov !== null) {
      for (const [k, c] of Object.entries(ov)) {
        if (k in CODE_TOKEN_NAMES && isHexColour(c)) out.overrides[m][k as CodeTokenKey] = c
      }
    }
  }
  return out
}

/** The tokens of theme `id` for `mode`, with `overrides` laid over them. An unknown id is Blerg. */
export function resolveCodeTokens(id: string, mode: CodeMode, overrides: Partial<CodeTokens> = {}): CodeTokens {
  const theme = codeThemes[id] ?? codeThemes.blerg
  const base = mode === 'light' && theme.light ? theme.light : theme.tokens
  const out = { ...base }
  for (const k of CODE_TOKEN_KEYS) {
    const o = overrides[k]
    if (isHexColour(o)) out[k] = o
  }
  return out
}

/** The custom properties that apply `colours` in `mode`, ready for an inline style. */
export function codeColourVars(colours: CodeColours, mode: CodeMode): Record<string, string> {
  const toks = resolveCodeTokens(colours[mode], mode, colours.overrides[mode])
  return Object.fromEntries(CODE_TOKEN_KEYS.map(k => [CODE_TOKEN_NAMES[k], toks[k]]))
}

/** Set a theme's custom properties on `el` (the document root, or a preview's wrapper). */
export function applyCodeTheme(el: HTMLElement, themeId: string, mode: CodeMode, overrides: Partial<CodeTokens> = {}): void {
  const toks = resolveCodeTokens(themeId, mode, overrides)
  for (const k of CODE_TOKEN_KEYS) el.style.setProperty(CODE_TOKEN_NAMES[k], toks[k])
}

/** Apply a person's whole choice for `mode` on `el`. */
export function applyCodeColours(el: HTMLElement, colours: CodeColours, mode: CodeMode): void {
  applyCodeTheme(el, colours[mode], mode, colours.overrides[mode])
}

/** The mode the page shows now: an explicit data-theme on the root wins, else the OS preference. */
export function currentCodeMode(): CodeMode {
  if (typeof document === 'undefined') return 'dark'
  const pinned = document.documentElement.getAttribute('data-theme')
  if (pinned === 'light' || pinned === 'dark') return pinned
  return typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
}
