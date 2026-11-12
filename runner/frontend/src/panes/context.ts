import { createContext, useCallback, useContext, useEffect, useMemo, useRef } from 'react'
import { signedInAccount } from '../lib/liveBoardCard'
import { useToastStore } from '../hooks/useToastStore'
import { usePaletteCommands } from '../commands/context'

export type PaneHandle = { open: boolean; restoreOpen(open: boolean): void }
export const PaneContext = createContext<{ focused: boolean; register(id: string, handle: { current: PaneHandle }): () => void; restore(): void; toggle(): void } | null>(null)

export function usePaneControl(id: string, title: string, { destructive, here }: { destructive: boolean; here?: number }, open: boolean, setOpen: (open: boolean) => void, enabled = true, scope = '', restoreOpen = setOpen) {
  const account = signedInAccount()
  const context = useContext(PaneContext)
  const identity = JSON.stringify([id, account, scope])
  const currentIdentity = useRef(identity)
  useEffect(() => { currentIdentity.current = identity }, [identity])
  const handle = useRef({ open, restoreOpen })
  useEffect(() => { handle.current = { open, restoreOpen } })
  const register = context?.register
  useEffect(() => enabled ? register?.(id, handle) : undefined, [enabled, register, id, account, scope])
  const change = useCallback((next: boolean) => { context?.restore(); setOpen(next); if (!next) useToastStore.getState().addToast({ title: 'Panel hidden', body: '', action: { label: 'Undo', onClick: () => { if (currentIdentity.current === identity) setOpen(true) } }, ttlMs: 8000 }) }, [context, setOpen, identity])
  const visible = open && !context?.focused
  const toggle = useCallback(() => change(!visible), [change, visible])
  const commands = useMemo(() => enabled ? [{ id: `toggle-${id}`, title, destructive, here, shortcutGroup: 'Panes' as const, shortcut: id === 'session-list' ? 'Ctrl/⌘+B' : undefined, run: toggle }] : [], [enabled, id, title, destructive, here, toggle])
  usePaletteCommands(commands)
  return { open: visible, setOpen: change, toggle, restoreOpen }
}

/** The folder control joins the session rail while the list is collapsed. */
export const SessionToolsHost = createContext<{ host: HTMLElement | null; setHost(host: HTMLElement | null): void } | null>(null)
