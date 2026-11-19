// BLG-452 review follow-up: one source of truth for what this renderer actually draws. Each mark
// file exports a MarkDeclaration describing exactly the channel types, aggregates, stack/sort
// values and mark-object properties its own drawXxx() function consumes — not what classify.ts's
// grammar table happens to allow. classify.ts is rewritten to build its per-mark allow-lists
// FROM these declarations (the union across all shipped marks), so a channel/property/value a
// mark doesn't actually read can never again classify 'blerg' while being silently ignored.
//
// Marks do not consume scale/legend configuration. These remain fallbacks.
// The shared guide layout consumes x/y title, format, tickCount, grid and
// labelAngle; specialized marks retain their narrower contract in classify.ts.
export type ChannelType = 'quantitative' | 'nominal' | 'ordinal' | 'temporal'

export interface ChannelDeclaration {
  /** /type values this mark's drawing code actually branches on for this channel. */
  types: ReadonlySet<ChannelType>
  /** /aggregate values honored for this channel (quantitative aggregation only). */
  aggregate?: ReadonlySet<string>
  /** Whether /bin is read for this channel. */
  bin?: boolean
  /** /stack values this mark's own stacking code actually implements (position channels only). */
  stack?: ReadonlySet<string>
  /** Whether a non-default (non-ascending, non-absent) /sort is honored for this channel. */
  sort?: boolean
}

export interface MarkDeclaration {
  mark: string
  /** Dependent channel shapes, beyond individual channel types. */
  supportsEncoding?: (encoding: Record<string, unknown>) => boolean
  /** Encoding channel name -> what's actually consumed for it. Only consumed channels appear. */
  channels: Partial<Record<string, ChannelDeclaration>>
  /** /mark/<property> keys this mark's draw function actually reads from the mark object
   *  (distinct from the encoding-channel forms of the same names, table row 4). Almost always
   *  empty today — render.ts doesn't even pass the raw mark object to most draw functions. */
  markProperties: ReadonlySet<string>
}
