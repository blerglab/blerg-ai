// Match #196's palette policy, including shadow-root targets and editor toolbars.
export function ownsAppShortcut(event: KeyboardEvent) {
  const surfaces = '[data-palette-shortcuts="ignore"], .xterm, .terminal-dom-surface, .monaco-editor, .cm-editor, .CodeMirror, .ace_editor'
  return [...event.composedPath(), document.activeElement].some(target => target instanceof Element && !!target.closest(surfaces))
}

export function modalIsOpen() {
  return [...document.querySelectorAll('[aria-modal="true"], dialog[open]')].some(modal =>
    !modal.closest('[hidden], [aria-hidden="true"]') && getComputedStyle(modal).display !== 'none')
}

export function visibleSurfaceIsOpen(selector: string, exclude?: string) {
  return [...document.querySelectorAll(selector)].some(modal => {
    if (exclude && modal.closest(exclude)) return false
    // Visibility is inherited but descendants can restore it; inspect the modal itself.
    const visibility = getComputedStyle(modal).visibility
    if (visibility === 'hidden' || visibility === 'collapse') return false
    // Retained tabs and React Activity can hide a dialog through its ancestors.
    // Accessibility masks are also authoritative, even when CSS still paints it.
    for (let ancestor: Element | null = modal; ancestor; ancestor = ancestor.parentElement) {
      if (ancestor.matches('[inert], [hidden], [aria-hidden="true"]')) return false
      const style = getComputedStyle(ancestor)
      if (style.display === 'none' || style.contentVisibility === 'hidden' || style.opacity === '0') return false
    }
    // Native checks also handle skipped content-visibility:auto subtrees. Keep
    // the ancestor checks above as a fallback for browsers without this API.
    return typeof modal.checkVisibility !== 'function' || modal.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true, contentVisibilityAuto: true })
  })
}

export function tabFocusShortcut(event: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey'>) {
  const mac = /Mac|iPhone|iPad|iPod/.test(navigator.platform)
  return event.key === 'Enter' && event.shiftKey && !event.altKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey)
}
/** The platform's palette and tab focus bindings are reserved inside a shell. */
export function shellPaletteShortcut(event: Pick<KeyboardEvent, 'key' | 'ctrlKey' | 'metaKey' | 'altKey' | 'shiftKey'>) {
  const mac = /Mac|iPhone|iPad|iPod/.test(navigator.platform)
  return tabFocusShortcut(event) || !event.altKey && !event.shiftKey && (mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey) && ['k', 'p'].includes(event.key.toLowerCase())
}
export function inShellTerminal(event: KeyboardEvent) {
  return [...event.composedPath(), document.activeElement].some(target => target instanceof Element && !!target.closest('.shell-terminal .xterm'))
}
