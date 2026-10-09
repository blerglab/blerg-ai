// The theming contract as a typed object, for an app that would rather hand ChatView its colours
// than write a stylesheet. Each key is one --chat-* custom property (theme/tokens.css); themeStyle
// turns the object into the inline style that sets them on the chat's root element.
import type { CSSProperties } from 'react'

/** Every token of the contract, by its property name. */
export const TOKEN_NAMES = {
  bg: '--chat-bg',
  surface: '--chat-surface',
  surface2: '--chat-surface-2',
  fg: '--chat-fg',
  muted: '--chat-muted',
  muted2: '--chat-muted-2',
  accent: '--chat-accent',
  accentFg: '--chat-accent-fg',
  success: '--chat-success',
  warning: '--chat-warning',
  info: '--chat-info',
  danger: '--chat-danger',
  border: '--chat-border',
  radius: '--chat-radius',
  font: '--chat-font',
  fontMono: '--chat-font-mono',
  textXs: '--chat-text-xs',
  textSm: '--chat-text-sm',
  density: '--chat-density',
  cardUserBg: '--chat-card-user-bg',
  cardAssistantBg: '--chat-card-assistant-bg',
  cardToolBg: '--chat-card-tool-bg',
  cardSystemBg: '--chat-card-system-bg',
} as const

export type ThemeTokenName = keyof typeof TOKEN_NAMES

/** The tokens an app sets. Any it leaves out keep the value of the stylesheet in effect (blerg.css,
 *  the app's own, or the fallback in tokens.css). Values are CSS as written: a colour, a length, a
 *  font stack, a number for density. */
export type ThemeTokens = Partial<Record<ThemeTokenName, string>>

/** The inline style that applies `tokens`: one custom property per token that is set. */
export function themeStyle(tokens: ThemeTokens): CSSProperties {
  const style: Record<string, string> = {}
  for (const key of Object.keys(TOKEN_NAMES) as ThemeTokenName[]) {
    const v = tokens[key]
    if (v !== undefined) style[TOKEN_NAMES[key]] = v
  }
  return style as CSSProperties
}
