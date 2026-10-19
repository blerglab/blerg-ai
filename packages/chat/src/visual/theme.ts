// Resolve the chat's inherited theme at the rendering surface. Only canonical colors,
// bounded sizes and a conservative font-family grammar enter renderer-generated CSS.
export interface VisualTheme {
  fg: string
  muted: string
  surface: string
  surface2: string
  border: string
  accent: string
  fontFamily: string
  fontSize: number
  palette: readonly string[]
  dark: boolean
}

const FALLBACK = {
  fg: '#f0f3f7', muted: '#bfc7d2', surface: '#171b22', surface2: '#1f242d',
  border: '#313a48', accent: '#ff6a3d',
  palette: ['#b69cf0', '#8fc9a0', '#e0b36a', '#7fb2ee', '#6fc7c7'],
}
const unsafeCss = /[;{}<>\\@]|url\s*\(|(?:https?|data|javascript|file):/i
const byte = (number: number) => Math.max(0, Math.min(255, Math.round(number)))
const hex = (parts: readonly number[]) => '#' + parts.map(value => byte(value).toString(16).padStart(2, '0')).join('')
const channels = (color: string) => [1, 3, 5].map(index => parseInt(color.slice(index, index + 2), 16))
const mix = (base: string, tint: string, strength: number) => hex(channels(base).map((value, i) => value * (1 - strength) + channels(tint)[i] * strength))

function parseColor(value: string, background: string): string | null {
  const short = /^#([\da-f]{3}|[\da-f]{4})$/i.exec(value)
  const expanded = short ? '#' + [...short[1]].map(char => char + char).join('') : value
  const match = /^#([\da-f]{6})([\da-f]{2})?$/i.exec(expanded)
  if (match) return match[2] ? mix(background, '#' + match[1], parseInt(match[2], 16) / 255) : '#' + match[1].toLowerCase()
  const rgb = /^(?:rgba?\(|color\(srgb\s+)([\d.%+\-eE,\s/]+)\)$/i.exec(value)
  if (!rgb) return null
  const parts = rgb[1].trim().split(/[\s,/]+/)
  if (parts.length !== 3 && parts.length !== 4) return null
  const srgb = /^color/i.test(value)
  const nums = parts.map((part, i) => Number.parseFloat(part) * (part.endsWith('%') ? (i === 3 ? 0.01 : 2.55) : (srgb && i < 3 ? 255 : 1)))
  if (nums.some(number => !Number.isFinite(number))) return null
  const color = hex(nums.slice(0, 3))
  return nums.length === 4 ? mix(background, color, Math.max(0, Math.min(1, nums[3]))) : color
}

function luminance(color: string): number {
  const [r, g, b] = channels(color).map(value => {
    const channel = value / 255
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
  })
  return r * 0.2126 + g * 0.7152 + b * 0.0722
}
function contrast(a: string, b: string): number {
  const x = luminance(a), y = luminance(b)
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05)
}
function readable(preferred: string, ...backgrounds: string[]): string {
  const score = (color: string) => Math.min(...backgrounds.map(background => contrast(color, background)))
  if (score(preferred) >= 4.5) return preferred
  let chosen = preferred
  for (let value = 0; value <= 255; value++) {
    const candidate = hex([value, value, value])
    if (score(candidate) > score(chosen)) chosen = candidate
  }
  return chosen
}

function safeFont(value: string): string {
  if (!value || value.length > 256 || unsafeCss.test(value) || [...value].some(char => char.charCodeAt(0) < 32 || (char.charCodeAt(0) >= 127 && char.charCodeAt(0) <= 159))) return 'system-ui, sans-serif'
  const families = value.split(',').map(family => family.trim())
  const family = /^(?:"[a-z\d _.-]+"|'[a-z\d _.-]+'|-?[a-z][a-z\d _.-]*)$/i
  return families.length <= 12 && families.every(value => family.test(value)) ? families.join(', ') : 'system-ui, sans-serif'
}

export function readVisualTheme(element: HTMLElement): VisualTheme {
  const window = element.ownerDocument.defaultView
  if (!window) throw new Error('Visual theme needs a document')
  const styles = window.getComputedStyle(element)
  const token = (name: string) => styles.getPropertyValue('--chat-' + name).trim()
  const probe = element.ownerDocument.createElement('span')
  probe.style.cssText = 'position:absolute;visibility:hidden;pointer-events:none'
  probe.setAttribute('aria-hidden', 'true')
  element.append(probe)
  try {
    function color(name: string, fallback: string, background = fallback): string {
      const value = token(name)
      if (!value || value.length > 256 || unsafeCss.test(value)) return fallback
      const direct = parseColor(value, background)
      if (direct) return direct
      // The color property cannot load a resource. Read it through the CSSOM to
      // resolve named colors, inherited currentColor and modern color functions.
      probe.style.color = ''
      probe.style.color = value
      if (!probe.style.color) return fallback
      return parseColor(window!.getComputedStyle(probe).color, background) ?? fallback
    }
    const surface = color('surface', FALLBACK.surface)
    const surface2 = color('surface-2', FALLBACK.surface2, surface)
    const fg = readable(color('fg', FALLBACK.fg, surface), surface, surface2)
    const muted = readable(color('muted', FALLBACK.muted, surface), surface, surface2)
    const border = color('border', FALLBACK.border, surface)
    const accent = color('accent', FALLBACK.accent, surface)
    const palette = Array.from(new Set([accent, ...['keyword', 'string', 'number', 'title', 'type'].map((name, i) => color('code-' + name, FALLBACK.palette[i], surface))]))
    const size = token('text-sm')
    probe.style.fontSize = ''
    if (size.length <= 128 && !unsafeCss.test(size)) probe.style.fontSize = size
    const resolvedSize = window.getComputedStyle(probe).fontSize
    const numericSize = /^([\d.]+)px$/.exec(resolvedSize || size)
    const fontSize = numericSize && Number.isFinite(Number(numericSize[1])) ? Math.max(8, Math.min(32, Number(numericSize[1]))) : 13
    return {fg, muted, surface, surface2, border, accent, palette, fontSize,
      fontFamily: safeFont(token('font')), dark: luminance(surface) < 0.4}
  } finally {
    probe.remove()
  }
}

export function mermaidTheme(theme: VisualTheme) {
  const secondary = mix(theme.surface2, theme.palette[1] ?? theme.accent, 0.12)
  const tertiary = mix(theme.surface2, theme.palette[2] ?? theme.accent, 0.12)
  const active = mix(theme.surface2, theme.accent, 0.16)
  return {theme: 'base' as const, themeVariables: {
    darkMode: theme.dark, background: 'transparent', fontFamily: theme.fontFamily, fontSize: `${theme.fontSize}px`,
    primaryColor: theme.surface2, primaryBorderColor: theme.border, primaryTextColor: theme.fg,
    secondaryColor: secondary, secondaryBorderColor: theme.border, secondaryTextColor: readable(theme.fg, secondary),
    tertiaryColor: tertiary, tertiaryBorderColor: theme.border, tertiaryTextColor: readable(theme.fg, tertiary),
    textColor: theme.fg, titleColor: theme.fg, lineColor: theme.muted, arrowheadColor: theme.muted,
    edgeLabelBackground: theme.surface, mainBkg: theme.surface2, nodeBkg: theme.surface2, nodeBorder: theme.border,
    nodeTextColor: theme.fg, clusterBkg: theme.surface, clusterBorder: theme.border,
    actorBkg: theme.surface2, actorBorder: theme.border, actorTextColor: theme.fg, actorLineColor: theme.muted,
    signalColor: theme.muted, signalTextColor: theme.fg, labelBoxBkgColor: theme.surface,
    labelBoxBorderColor: theme.border, labelTextColor: theme.fg, loopTextColor: theme.fg,
    activationBkgColor: secondary, activationBorderColor: theme.border, sequenceNumberColor: theme.fg,
    noteBkgColor: theme.surface, noteBorderColor: theme.border, noteTextColor: theme.fg,
    stateBkg: theme.surface2, stateLabelColor: theme.fg, transitionColor: theme.muted,
    transitionLabelColor: theme.fg, labelBackgroundColor: theme.surface,
    compositeBackground: theme.surface, compositeTitleBackground: theme.surface2, compositeBorder: theme.border,
    altBackground: theme.surface2, relationColor: theme.muted, relationLabelColor: theme.fg,
    relationLabelBackground: theme.surface, rowOdd: theme.surface2, rowEven: theme.surface,
    taskBkgColor: theme.surface2, taskBorderColor: theme.border, taskTextColor: theme.fg,
    taskTextDarkColor: readable(theme.fg, theme.surface2, active), taskTextLightColor: theme.fg,
    taskTextOutsideColor: theme.fg, taskTextClickableColor: theme.fg,
    activeTaskBkgColor: active, activeTaskBorderColor: theme.accent,
    doneTaskBkgColor: theme.surface2, doneTaskBorderColor: theme.border,
    critBkgColor: active, critBorderColor: theme.accent, todayLineColor: theme.accent,
    sectionBkgColor: theme.surface, sectionBkgColor2: theme.surface2, altSectionBkgColor: theme.surface,
    excludeBkgColor: theme.surface, rectBkgColor: theme.surface, gridColor: theme.border, vertLineColor: theme.muted,
  }}
}

export function vegaTheme(theme: VisualTheme) {
  return {background: 'transparent', font: theme.fontFamily, view: {stroke: null},
    mark: {color: theme.accent},
    axis: {labelColor: theme.muted, titleColor: theme.fg, domainColor: theme.border, tickColor: theme.border,
      gridColor: theme.border, gridOpacity: 0.35, labelFontSize: theme.fontSize, titleFontSize: theme.fontSize},
    legend: {labelColor: theme.fg, titleColor: theme.fg, labelFontSize: theme.fontSize, titleFontSize: theme.fontSize},
    title: {color: theme.fg, subtitleColor: theme.muted, fontSize: theme.fontSize + 1, subtitleFontSize: theme.fontSize},
    text: {color: theme.fg, fontSize: theme.fontSize}, range: {category: [...theme.palette]}}
}

/** Observe only inherited-theme inputs, never renderer children. A burst is sampled once
 * per frame and callbacks fire only when the resolved safe theme actually changed. */
export function observeVisualTheme(element: HTMLElement, onChange: (theme: VisualTheme) => void): () => void {
  const window = element.ownerDocument.defaultView
  if (!window) return () => {}
  let current = JSON.stringify(readVisualTheme(element))
  let disposed = false
  let scheduled: number | null = null
  const raf = (check: FrameRequestCallback) => window.requestAnimationFrame ? window.requestAnimationFrame(check) : window.setTimeout(() => check(0), 16)
  const cancel = (id: number) => window.cancelAnimationFrame ? window.cancelAnimationFrame(id) : window.clearTimeout(id)
  const check = () => {
    scheduled = null
    if (disposed) return
    const theme = readVisualTheme(element)
    const next = JSON.stringify(theme)
    if (next !== current) { current = next; onChange(theme) }
  }
  const schedule = () => { if (!disposed && scheduled === null) scheduled = raf(check) }
  const observer = new window.MutationObserver(schedule)
  for (let node: HTMLElement | null = element; node; node = node.parentElement) {
    observer.observe(node, {attributes: true})
  }
  if (element.ownerDocument.head) observer.observe(element.ownerDocument.head, {subtree: true, childList: true, characterData: true, attributes: true})
  const media = window.matchMedia?.('(prefers-color-scheme: dark)')
  media?.addEventListener?.('change', schedule)
  window.addEventListener('resize', schedule)
  element.ownerDocument.head?.addEventListener('load', schedule, true)
  return () => {
    disposed = true
    observer.disconnect()
    if (scheduled !== null) cancel(scheduled)
    media?.removeEventListener?.('change', schedule)
    window.removeEventListener('resize', schedule)
    element.ownerDocument.head?.removeEventListener('load', schedule, true)
  }
}
