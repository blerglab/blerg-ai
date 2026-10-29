import type { ITheme } from '@xterm/xterm'

/** Resolve aliases after the app's explicit or system theme has been applied. */
export function shellTheme(element: Element): { theme: ITheme; fontFamily: string } {
  const css = getComputedStyle(element)
  const token = (name: string) => css.getPropertyValue(`--${name}`).trim()
  return {
    fontFamily: token('mono'),
    theme: {
      background: token('basalt'), foreground: token('chalk'), cursor: token('blaze'), cursorAccent: token('basalt'),
      selectionBackground: token('selected-bg'), selectionForeground: token('chalk'),
      black: token('terminal-black'), brightBlack: token('terminal-bright-black'),
      red: token('terminal-red'), brightRed: token('terminal-bright-red'),
      green: token('terminal-green'), brightGreen: token('terminal-bright-green'),
      yellow: token('terminal-yellow'), brightYellow: token('terminal-bright-yellow'),
      blue: token('terminal-blue'), brightBlue: token('terminal-bright-blue'),
      magenta: token('terminal-magenta'), brightMagenta: token('terminal-bright-magenta'),
      cyan: token('terminal-cyan'), brightCyan: token('terminal-bright-cyan'),
      white: token('terminal-white'), brightWhite: token('terminal-bright-white'),
    },
  }
}
