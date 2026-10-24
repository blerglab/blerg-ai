import { createContext, useContext, useEffect } from 'react'
import { CommandRegistry, type PaletteItem, type PaletteMode, type PaletteProvider } from './registry'

export const PaletteContext = createContext<{ registry: CommandRegistry; open(mode: PaletteMode): void } | null>(null)
const NO_PROVIDERS: PaletteProvider[] = []
export function usePalette() { return useContext(PaletteContext) }
/** Pass memoized arrays: registration follows the feature's lifetime and permissions. */
export function usePaletteCommands(items: PaletteItem[], providers: PaletteProvider[] = NO_PROVIDERS) {
  const palette = usePalette()
  useEffect(() => palette?.registry.register(items, providers), [palette, items, providers])
}
