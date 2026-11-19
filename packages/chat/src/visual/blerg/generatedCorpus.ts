// BLG-452 review follow-up (#2, tightened): the generated corpus itself. Test-only (like
// differential.ts) — exported from here so both the fast jsdom smoke test
// (generatedCoverage.test.ts) and the real-browser differential gate
// (runner/frontend's GeneratedCorpus.differential.browser.test.tsx) generate the exact same
// cases from the exact same source, rather than two generators that could drift apart.
import { declaration as barDeclaration } from './marks/bar'
import { declarations as lineDeclarations } from './marks/line'
import { declarations as pointDeclarations } from './marks/point'
import { declaration as textDeclaration } from './marks/text'
import { declaration as ruleDeclaration } from './marks/rule'
import { declaration as rectDeclaration } from './marks/rect'
import { declaration as arcDeclaration } from './marks/arc'
import { declaration as boxplotDeclaration } from './marks/boxplot'
import type { ChannelType, MarkDeclaration } from './declare'

export const ALL_DECLARATIONS: readonly MarkDeclaration[] = [
  barDeclaration, ...lineDeclarations, ...pointDeclarations, textDeclaration, ruleDeclaration, rectDeclaration, arcDeclaration, boxplotDeclaration,
]

/** A sample field name + row generator for a given declared type, reused across every mark so
 *  one small table covers the whole corpus. */
function sampleField(type: ChannelType): {field: string; rows: Array<Record<string, unknown>>} {
  switch (type) {
    case 'quantitative': return {field: 'q', rows: [{q: 1}, {q: 4}, {q: 2}]}
    case 'nominal': return {field: 'n', rows: [{n: 'A'}, {n: 'B'}, {n: 'C'}]}
    case 'ordinal': return {field: 'o', rows: [{o: 'A'}, {o: 'B'}, {o: 'C'}]}
    case 'temporal': return {field: 't', rows: [{t: '2024-01-01'}, {t: '2024-02-01'}]}
  }
}

/** Each mark's own required-channel base case — the minimal spec every generated variant starts
 *  from, built directly from the mark's declared required channels (x/y for the position marks,
 *  theta for arc, x/y/y2 for rule). Declarations don't distinguish required vs. optional
 *  channels, so this table names exactly what render.ts's own guards (missing-field checks)
 *  require, not a second copy of the declaration. */
const BASE: Record<string, {spec: Record<string, unknown>; mergeRows: (extra: Record<string, unknown>) => Array<Record<string, unknown>>}> = (() => {
  const barRows = [{x: 'A', y: 3}, {x: 'B', y: 4}, {x: 'C', y: 2}]
  const posRows = [{x: 1, y: 2}, {x: 3, y: 4}, {x: 2, y: 1}]
  const ruleRows = [{x: 'A', y: 1, y2: 5}, {x: 'B', y: 2, y2: 6}]
  const rectRows = [{x: 'A', y: '1', c: 2}, {x: 'B', y: '1', c: 8}, {x: 'A', y: '2', c: 4}]
  const arcRows = [{t: 3}, {t: 7}, {t: 5}]
  const boxRows = Array.from({length: 9}, (_, i) => ({x: i % 3 === 0 ? 'A' : i % 3 === 1 ? 'B' : 'C', y: i}))
  return {
    bar: {spec: {mark: 'bar', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}}}, mergeRows: extra => barRows.map(r => ({...r, ...extra}))},
    line: {spec: {mark: 'line', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}}}, mergeRows: extra => posRows.map(r => ({...r, ...extra}))},
    area: {spec: {mark: 'area', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}}}, mergeRows: extra => posRows.map(r => ({...r, ...extra}))},
    point: {spec: {mark: 'point', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}}}, mergeRows: extra => posRows.map(r => ({...r, ...extra}))},
    circle: {spec: {mark: 'circle', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}}}, mergeRows: extra => posRows.map(r => ({...r, ...extra}))},
    square: {spec: {mark: 'square', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}}}, mergeRows: extra => posRows.map(r => ({...r, ...extra}))},
    text: {spec: {mark: 'text', encoding: {x: {field: 'x', type: 'quantitative'}, y: {field: 'y', type: 'quantitative'}, text: {field: 'x', type: 'quantitative'}}}, mergeRows: extra => posRows.map(r => ({...r, ...extra}))},
    rule: {spec: {mark: 'rule', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}, y2: {field: 'y2', type: 'quantitative'}}}, mergeRows: extra => ruleRows.map(r => ({...r, ...extra}))},
    rect: {spec: {mark: 'rect', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'nominal'}, color: {field: 'c', type: 'quantitative'}}}, mergeRows: extra => rectRows.map(r => ({...r, ...extra}))},
    arc: {spec: {mark: 'arc', encoding: {theta: {field: 't', type: 'quantitative'}}}, mergeRows: extra => arcRows.map(r => ({...r, ...extra}))},
    boxplot: {spec: {mark: 'boxplot', encoding: {x: {field: 'x', type: 'nominal'}, y: {field: 'y', type: 'quantitative'}}}, mergeRows: extra => boxRows.map(r => ({...r, ...extra}))},
  }
})()

export interface Case { label: string; mark: string; spec: Record<string, unknown> }

export function generateCases(): Case[] {
  const cases: Case[] = []
  for (const declaration of ALL_DECLARATIONS) {
    const base = BASE[declaration.mark]
    // Review finding (#2): this used to silently `continue`, so a declared mark with no BASE
    // entry drew zero generated cases for every one of its declared options without the build
    // ever noticing — fail loudly instead; add a BASE entry, don't skip one.
    if (!base) throw new Error(`generatedCorpus: no BASE spec for declared mark "${declaration.mark}" — its declared options would get zero generated cases`)
    const baseEncoding = (base.spec.encoding as Record<string, unknown>)
    for (const [channel, channelDeclaration] of Object.entries(declaration.channels)) {
      // Review finding (#2): this used to also skip a channel already present in the base
      // encoding ("required channels are already exercised by every case") — but the base case
      // only exercises ONE of a required channel's declared types (e.g. bar's required x is
      // always nominal there, never ordinal, even though bar declares both). Every declared type
      // on every channel — required or not — now gets its own case, pointed at a fresh field of
      // that exact type, same mechanism as an optional channel's.
      if (!channelDeclaration) continue
      for (const type of channelDeclaration.types) {
        const {field, rows: sampleRows} = sampleField(type)
        // xOffset only draws alongside a color channel (bar.ts's own documented requirement —
        // grouped bars are, by definition, grouped *by* a category, same as Vega-Lite's own
        // grammar); the generator adds it so this exercises xOffset itself, not that guard.
        let companion: Record<string,unknown> = channel === 'xOffset' ? {color: {field: 'n', type: 'nominal'}} : {}
        // Review finding (#2, found while fixing it): a single sample *value* repeated on every
        // row (the old behavior) is fine for a channel that doesn't group/position anything
        // (color, tooltip), but collapses a *required* position channel's rows into one category
        // — bar.x:ordinal drew one 3-row-tall bar, not three, since every row got the same 'A'.
        // Cycling through sampleField's own row set gives each row its own value, same shape as
        // the base case's own rows.
        let values = base.mergeRows({n: 'A'}).map((row, i) => ({...row, ...sampleRows[i % sampleRows.length]}))
        if(declaration.mark==='bar' && (channel==='x' && type==='quantitative' || channel==='x2')) {
          companion={x:{field:'lower',type:'quantitative',bin:'binned'},x2:{field:'upper',type:'quantitative'}}
          values=values.map((row,i)=>({...row,q:i*2+(channel==='x2'?2:0),lower:i*2,upper:i*2+2}))
          if(channel==='x') companion.x2={field:'upper',type:'quantitative'}
        }
        // Interval positions need both bounds and categorical status, not the heatmap base.
        if (declaration.mark==='rect' && ((channel==='x' || channel==='x2') && ['quantitative','temporal'].includes(type) || channel==='color' && type!=='quantitative' && type!=='temporal')) {
          const positionType=channel==='color' ? 'temporal' : type
          companion={x:{field:'start',type:positionType},x2:{field:'end',type:positionType},color:{field:'status',type:'nominal'}}
          values=values.map((row,i)=>({...row,start:positionType==='temporal'?'2024-01-01T00:00:00Z':0,end:positionType==='temporal'?'2024-03-01T00:00:00Z':6,status:'Ready',...(channel==='color'?{}:{[field]:positionType==='temporal'?String(sampleRows[i%sampleRows.length][field])+'T00:00:00Z':sampleRows[i%sampleRows.length][field]})}))
        }
        cases.push({
          label: `${declaration.mark}.${channel}:${type}`, mark: declaration.mark,
          spec: {data: {values}, ...base.spec, encoding: {...baseEncoding, ...companion, [channel]: {field, type,...(declaration.mark==='bar' && channel==='x' && type==='quantitative'?{bin:'binned'}:{})}}},
        })
      }
      if (channelDeclaration.aggregate) {
        for (const aggregate of channelDeclaration.aggregate) {
          cases.push({
            label: `${declaration.mark}.${channel}.aggregate:${aggregate}`, mark: declaration.mark,
            spec: {data: {values: base.mergeRows({})}, ...base.spec, encoding: {...baseEncoding, [channel]: {...(baseEncoding[channel] as object), aggregate}}},
          })
        }
      }
      if (channelDeclaration.sort) {
        // Review finding (custom sort): this used to always reuse baseEncoding[channel] — fine
        // for a required channel (always present there), but color/xOffset aren't, so the
        // generated encoding had a sort with no field at all (classify.ts's own
        // "every channel needs a field" check then rejected it). Builds its own field the same
        // way the per-type loop above does when the channel isn't already in baseEncoding.
        // Also covers every sort form this review round implements (ascending was already
        // exercised by every other case; descending/null/array are the new ones), not just the
        // one literal 'ascending' value that needed no special permission to begin with.
        const inBase = channel in baseEncoding
        const sortType = declaration.mark==='rect' && channel==='color' ? 'nominal' : [...channelDeclaration.types][0]!
        const {field: sortField, rows: sortRows} = sampleField(sortType)
        let fieldDef = inBase ? (baseEncoding[channel] as {field: string; type: string}) : {field: sortField, type: sortType}
        let sortCompanion: Record<string,unknown> = channel === 'xOffset' ? {color: {field: 'n', type: 'nominal'}} : {}
        let sortValues = inBase
          ? base.mergeRows({})
          : base.mergeRows({n: 'A'}).map((row, i) => ({...row, ...sortRows[i % sortRows.length]}))
        if(declaration.mark==='rect' && channel==='color'){
          fieldDef={field:'status',type:'nominal'}
          sortCompanion={x:{field:'start',type:'temporal'},x2:{field:'end',type:'temporal'}}
          sortValues=base.mergeRows({}).map((row,i)=>({...row,status:sortRows[i%sortRows.length][sortField],start:'2024-01-01T00:00:00Z',end:'2024-02-01T00:00:00Z'}))
        }
        const variants: Array<[string, unknown]> = [
          ['descending', 'descending'],
          ['none', null],
          ['array', [...sortRows].reverse().map(row => row[Object.keys(row)[0]!])],
        ]
        for (const [label, sort] of variants) {
          cases.push({
            label: `${declaration.mark}.${channel}.sort:${label}`, mark: declaration.mark,
            spec: {data: {values: sortValues}, ...base.spec, encoding: {...baseEncoding, ...sortCompanion, [channel]: {...fieldDef, sort}}},
          })
        }
      }
    }
    // The stack+color pair the review named: bar/area with an explicit color channel and each
    // declared stack value present at once (not just each alone).
    const yDeclaration = declaration.channels.y
    if (yDeclaration?.stack && declaration.channels.color) {
      for (const stack of yDeclaration.stack) {
        cases.push({
          label: `${declaration.mark}.stack+color:${stack}`, mark: declaration.mark,
          spec: {
            data: {values: base.mergeRows({}).map((r, i) => ({...r, c: i % 2 === 0 ? 'A' : 'B'}))},
            ...base.spec,
            encoding: {...baseEncoding, y: {...(baseEncoding.y as object), stack}, color: {field: 'c', type: 'nominal'}},
          },
        })
      }
    }
  }
  // The layer+each-mark pair the review named: both supported layer combos, end to end.
  const barText = {mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}
  cases.push({label: 'layer:bar+text', mark: 'layer', spec: {
    data: {values: [{month: 'Jan', open: 3}, {month: 'Feb', open: 4}]}, encoding: barText.encoding,
    layer: [{mark: 'bar'}, {mark: 'text', encoding: {text: {field: 'open', type: 'quantitative'}}}],
  }})
  cases.push({label: 'layer:line+rule', mark: 'layer', spec: {
    data: {values: [{t: 1, v: 10}, {t: 2, v: 15}, {t: 3, v: 12}]},
    layer: [{mark: 'line', encoding: {x: {field: 't', type: 'quantitative'}, y: {field: 'v', type: 'quantitative'}}}, {mark: 'rule', encoding: {y: {datum: 12}}}],
  }})
  cases.push({label: 'layer:area+rule', mark: 'layer', spec: {
    data: {values: [{t: 1, v: 10}, {t: 2, v: 15}, {t: 3, v: 12}]},
    layer: [{mark: 'area', encoding: {x: {field: 't', type: 'quantitative'}, y: {field: 'v', type: 'quantitative'}}}, {mark: 'rule', encoding: {y: {datum: 12}}}],
  }})
  // Review finding (#1): "Sweep several domains/sizes per mark — generatedCorpus.ts's own BASE
  // table used a single row set, so a density-tick-count bracket boundary only this one domain
  // happened to hit (line's own x:[1,3] at width 640) went uncaught. Swept across every mark
  // whose continuous axis feeds densityTickCount (point/circle/square/text/line/area) — a mark
  // whose own BASE case already covers the "normal" domain, this adds a zero-anchored wide span,
  // a large-number span and a sub-unit span, the three shapes most likely to land on a different
  // "nice step" bracket than the single hand-picked row set did.
  const DOMAIN_SWEEP_ROWS: Array<Array<Record<string, unknown>>> = [
    [{x: 0, y: 0}, {x: 10, y: 8}, {x: 5, y: 3}],
    [{x: 100, y: 200}, {x: 300, y: 400}, {x: 200, y: 100}],
    [{x: 0.1, y: 0.2}, {x: 0.3, y: 0.4}, {x: 0.2, y: 0.1}],
  ]
  for (const mark of ['point', 'circle', 'square', 'text', 'line', 'area']) {
    const base = BASE[mark]
    for (const [i, rows] of DOMAIN_SWEEP_ROWS.entries()) {
      cases.push({label: `${mark}.domain-sweep:${i}`, mark, spec: {data: {values: rows}, ...base.spec}})
    }
  }
  return cases
}
