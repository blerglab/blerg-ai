// The card registry: which kinds an app draws, and with what. The package defines no kind; the
// app does (defineCard), lists them (createCardRegistry) and hands the registry to ChatView. The
// markdown renderer asks it for each card fence it meets and draws a plain code block for a kind
// it does not know, so nothing the model wrote is ever hidden.
import type { ComponentType } from 'react'

/** What a card component receives: the fence's kind and its parsed body. The data is model
 *  output — a component renders its fields as text (never HTML) and puts links through safeUrl. */
export interface CardProps {
  kind: string
  data: Record<string, unknown>
}

export interface CardDefinition {
  kind: string
  Component: ComponentType<CardProps>
}

export interface CardRegistry {
  /** The definition for a kind, or undefined for one the app did not define. */
  get(kind: string): CardDefinition | undefined
}

export function defineCard(kind: string, Component: ComponentType<CardProps>): CardDefinition {
  return { kind, Component }
}

/** A registry over `defs`. A kind defined twice takes the later definition. */
export function createCardRegistry(defs: CardDefinition[]): CardRegistry {
  // A Map, not an object: the kind is whatever the model wrote after `card:`, and "constructor"
  // must not find anything.
  const byKind = new Map<string, CardDefinition>()
  for (const d of defs) byKind.set(d.kind, d)
  return { get: kind => byKind.get(kind) }
}
