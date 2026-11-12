export function platformShortcut(shortcut: string, platform = navigator.platform) {
  return shortcut.replace(/Ctrl\s*\/\s*⌘/g, /Mac|iPhone|iPad|iPod/.test(platform) ? '⌘' : 'Ctrl')
}
export function helpShortcut(event: KeyboardEvent) {
  if (event.key !== '?' || event.ctrlKey || event.metaKey || event.altKey || event.repeat || event.isComposing || event.keyCode === 229 || event.defaultPrevented) return false
  const typing = 'input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="combobox"], [data-palette-shortcuts="ignore"], .xterm, .terminal-dom-surface, .monaco-editor, .cm-editor, .CodeMirror, .ace_editor'
  return ![...event.composedPath(), document.activeElement].some(target => target instanceof Element && !!target.closest(typing))
}

