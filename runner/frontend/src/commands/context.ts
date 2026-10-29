import { createContext, useContext, useLayoutEffect, useRef } from 'react'
import { CommandRegistry, assertCommandSafety, type PaletteCommand, type PaletteMode, type PaletteProvider } from './registry'

export const PaletteContext = createContext<{ registry: CommandRegistry; open(mode: PaletteMode): void } | null>(null)
const NO_PROVIDERS: PaletteProvider[] = []
export function usePalette() { return useContext(PaletteContext) }
/** Update atomically; only disposing the feature removes its commands and providers. */
export function usePaletteCommands(items: PaletteCommand[], providers: PaletteProvider[] = NO_PROVIDERS) {
  const palette = usePalette()
  const registry = palette?.registry
  const registration = useRef<ReturnType<CommandRegistry['register']> | null>(null)
  useLayoutEffect(() => {
    if (!registry) return
    const dispose = registry.register([], NO_PROVIDERS)
    registration.current = dispose
    return () => { dispose(); registration.current = null }
  }, [registry])
  useLayoutEffect(() => { assertCommandSafety(items); registration.current?.update(items, providers) }, [registry, items, providers])
}
