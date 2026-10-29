import { useEffect, useMemo, useRef } from 'react'
import { usePaletteCommands } from '../commands/context'
import { ownsAppShortcut, visibleSurfaceIsOpen } from '../commands/shortcutOwnership'
import { tabLabel, type SessionTabs } from './useSessionTabs'
import { toggleWrapIn } from '@blerglab/chat'

const blockingSurfaces = '[aria-modal="true"], dialog[open], [role="menu"], [popover]:popover-open, .confirm-popover'

/** Session tab bindings share the palette's capture claim, IME and editor ownership policy. */
export function useSessionTabCommands(sessionTabs: SessionTabs) {
  const { active, tabs, select, close, groups, focused, phone, split, nextGroup, targetFor, tabFocused } = sessionTabs
  const group = groups.find(group => group.id === focused)!
  const modifier = /Mac|iPhone|iPad|iPod/.test(navigator.platform) ? 'Control' : 'Alt'
  const commands = useMemo(() => {
    const ids = phone ? ['chat', ...tabs.map(tab => tab.id)] : group.tabs
    const choose = (id: string) => {
      select(id)
      // Selecting the current tab from another pane still brings its content back into focus.
      if (id === active && !phone) targetFor(id).querySelector<HTMLElement>(id === 'chat' ? '.agent-composer textarea' : '.xterm-helper-textarea, .hosted-view:not([hidden]) [role="region"]')?.focus({ preventScroll: true })
    }
    const step = (direction: number) => choose(ids[(ids.indexOf(active) + direction + ids.length) % ids.length])
    return [
      { id: 'tab-chat', destructive: false, title: 'Back to Chat', shortcut: ids[0] === 'chat' ? `Esc / ${modifier}+1` : 'Esc', run: () => choose('chat') },
      ...ids.slice(0, 9).map((id, index) => ({ id: `tab-number-${index + 1}`, destructive: false, title: `Go to tab ${index + 1}: ${id === 'chat' ? 'Chat' : tabLabel(tabs.find(tab => tab.id === id)!.view)}`, shortcut: `${modifier}+${index + 1}`, run: () => choose(id) })),
      { id: 'tab-next', destructive: false, title: 'Next tab', shortcut: `${modifier}+]`, run: () => step(1) },
      { id: 'tab-previous', destructive: false, title: 'Previous tab', shortcut: `${modifier}+[`, run: () => step(-1) },
      ...(!phone && !tabFocused ? [
        { id: 'group-split-right', destructive: false, title: 'Split tab group right', detail: group.tabs.length < 2 ? 'Open another tab in this group to split' : undefined, here: group.tabs.length < 2 ? undefined : 6, run: () => split('right') },
        { id: 'group-split-down', destructive: false, title: 'Split tab group down', detail: group.tabs.length < 2 ? 'Open another tab in this group to split' : undefined, run: () => split('bottom') },
        { id: 'group-move-next', destructive: false, title: 'Move tab to next group', detail: groups.length < 2 ? 'Split a group first' : undefined, run: () => nextGroup(true) },
        { id: 'group-focus-next', destructive: false, title: 'Focus next tab group', detail: groups.length < 2 ? 'Split a group first' : undefined, run: () => nextGroup() },
      ] : []),
      // The focused viewer's wrap toggle (#265): code, a raw file or a diff; nothing in other views.
      // Alt+Z itself belongs to the viewer (its own keydown), never to the window.
      ...(active === 'chat' || tabs.find(tab => tab.id === active)?.view.kind === 'shell' ? [] : [{ id: 'view-wrap', destructive: false, title: 'Toggle line wrap', shortcut: 'Alt+Z', run: () => { toggleWrapIn(targetFor(active).querySelector('.hosted-view:not([hidden])')) } }]),
      ...(active === 'chat' ? [] : [{ id: 'tab-close', destructive: tabs.find(tab => tab.id === active)?.view.kind === 'shell', title: 'Close current tab', shortcut: `${modifier}+W`, run: () => close(active) }]),
    ]
  }, [active, tabs, modifier, select, close, group, groups.length, phone, split, nextGroup, targetFor, tabFocused])
  usePaletteCommands(commands)
  // React may commit a preview/toolbar update before this native event reaches window.
  const claimed = useRef(new WeakMap<KeyboardEvent, string>())

  useEffect(() => {
    const run = (event: KeyboardEvent, id: string) => {
      event.stopImmediatePropagation()
      commands.find(command => command.id === id)?.run()
    }
    const keydown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && document.querySelector('.session-groups[data-tab-dragging]')) return
      if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || ownsAppShortcut(event)) return
      if ([...event.composedPath(), document.activeElement].some(node => node instanceof Element && node.closest('.review-editor, .tiptap, .document-toolbar, [contenteditable]:not([contenteditable="false"])'))) return
      let id: string | undefined
      if (event.key === 'Escape' && !event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey) {
        if (active === 'chat' || visibleSurfaceIsOpen(blockingSurfaces)) return
        const path = [...event.composedPath(), document.activeElement]
        if (path.some(node => node instanceof Element && node.closest('input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="combobox"], [role="menu"], [role="dialog"]'))) return
        if (!path.some(node => node instanceof Element && node.closest('.session-file-target, .session-tab'))) return
        id = 'tab-chat'
      } else {
        const modified = modifier === 'Control' ? event.ctrlKey && !event.altKey : event.altKey && !event.ctrlKey
        if (!modified || event.metaKey || event.shiftKey) return
        const key = event.key.toLowerCase()
        if (/^[1-9]$/.test(key)) id = `tab-number-${key}`
        else if (key === ']') id = 'tab-next'
        else if (key === '[') id = 'tab-previous'
        else if (key === 'w') id = 'tab-close'
        else return
      }
      // Claim every owned keydown, including repeats, unavailable slots and modal-blocked actions.
      event.preventDefault()
      if (event.repeat || visibleSurfaceIsOpen(blockingSurfaces)) return
      if (event.composedPath().some(node => node instanceof Element && node.matches('.agent-composer textarea, .xterm-helper-textarea'))) {
        event.stopPropagation()
        commands.find(command => command.id === id)?.run()
      } else claimed.current.set(event, id)
    }
    const bubble = (event: KeyboardEvent) => {
      const id = claimed.current.get(event)
      if (id) run(event, id)
    }
    window.addEventListener('keydown', keydown, true)
    window.addEventListener('keydown', bubble)
    return () => { window.removeEventListener('keydown', keydown, true); window.removeEventListener('keydown', bubble) }
  }, [active, commands, modifier])
}
