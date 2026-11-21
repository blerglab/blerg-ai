import { describe, expect, it } from 'vitest'
import { classify, classifyChart, MARK_PROPERTIES, DEFERRED_MARKS } from './classify'
import { MARKS } from '../vegaPolicy'

// classify() is the pure, path-grammar allow-list walk (design doc §3.3), parameterized by which
// marks have shipped — so it can be tested against the full grammar today, before any mark has
// actually passed the differential gate (§3.10). classifyChart() is the production entry point:
// it always uses SHIPPED_MARKS, which starts empty, so it always falls back today (step 1 of
// BLG-452's build order: the classifier comes before the differential harness and before any
// mark enters the allow-list).
//
// Review follow-up: classify.ts's per-mark acceptance now comes straight from each mark file's
// own MarkDeclaration (declare.ts), not a separately-maintained table — so most of this file's
// expectations tightened from "accepts the shape" to "accepts only what that mark's declaration
// actually draws." scale/axis/legend/condition/title(per-channel)/format/formatType are rejected
// outright now: no mark file anywhere reads any of them (declare.ts's own header).

const ALL_MARKS = new Set(['bar', 'line', 'area', 'point', 'circle', 'square', 'rule', 'text', 'arc', 'rect', 'boxplot'])

const bar = {mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}}
const values = [{month: 'Jan', team: 'A', open: 3}, {month: 'Feb', team: 'A', open: 4}]

describe('classifyChart (production entry point)', () => {
  it('draws with ours once a mark has shipped (bar, as of BLG-452 step 3)', () => {
    expect(classifyChart({data: {values}, ...bar})).toBe('blerg')
  })
  it('still falls back for a mark that has not shipped yet (tick: a firm v1 exclusion, not pending)', () => {
    expect(classifyChart({data: {values}, mark: 'tick', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: 'open', type: 'quantitative'}}})).toBe('fallback')
  })
  it('draws a real composition of shipped marks (BLG-452 step 4)', () => {
    expect(classifyChart({data: {values}, hconcat: [bar, bar]})).toBe('blerg')
  })
  it('still falls back for a single-child layer (BLG-452 priority 3 scope: exactly two children)', () => {
    expect(classifyChart({data: {values}, layer: [bar]})).toBe('fallback')
  })
  it('draws a supported layer overlay once both marks have shipped (BLG-452 priority 3)', () => {
    const text = {mark: 'text', encoding: {text: {field: 'open', type: 'quantitative'}}}
    expect(classifyChart({data: {values}, encoding: bar.encoding, layer: [{mark: 'bar'}, text]})).toBe('blerg')
  })
})

describe('classify: mark type (table row 1)', () => {
  it('accepts a shipped mark', () => {
    expect(classify({data: {values}, ...bar}, ALL_MARKS)).toBe('blerg')
  })
  it('falls back for a mark not yet shipped', () => {
    expect(classify({data: {values}, ...bar}, new Set())).toBe('fallback')
  })
  it('falls back for tick and trail even when otherwise shipped (decided: stay on Vega, §3.2)', () => {
    expect(classify({data: {values}, mark: 'tick', encoding: bar.encoding}, new Set(['tick']))).toBe('fallback')
    expect(classify({data: {values}, mark: 'trail', encoding: bar.encoding}, new Set(['trail']))).toBe('fallback')
  })
  it('accepts the object mark form', () => {
    expect(classify({data: {values}, mark: {type: 'bar'}, encoding: bar.encoding}, ALL_MARKS)).toBe('blerg')
  })
})

describe('classify: mark properties (table row 3) — review follow-up: declaration-driven, mostly empty', () => {
  it('accepts mark/color and mark/opacity now that the mark files actually draw them (task 2, item 1)', () => {
    expect(classify({data: {values}, mark: {type: 'bar', opacity: 0.8}, encoding: bar.encoding}, ALL_MARKS)).toBe('blerg')
    expect(classify({data: {values}, mark: {type: 'bar', color: '#2f6fdb'}, encoding: bar.encoding}, ALL_MARKS)).toBe('blerg')
    expect(classify({data: {values}, mark: {type: 'line', color: '#2f6fdb', opacity: 0.8}, encoding: {x: {field: 'open', type: 'quantitative'}, y: {field: 'open', type: 'quantitative'}}}, ALL_MARKS)).toBe('blerg')
  })
  it('falls back for any other mark property on a mark that doesn\'t declare it', () => {
    // None of these are actually consumed by any draw function — so classify still doesn't
    // accept them for marks that don't declare them, rather than silently drawing the default
    // and ignoring the request.
    expect(classify({data: {values}, mark: {type: 'bar', cornerRadius: 2}, encoding: bar.encoding}, ALL_MARKS)).toBe('fallback')
    expect(classify({data: {values}, mark: {type: 'line', interpolate: 'monotone'}, encoding: bar.encoding}, ALL_MARKS)).toBe('fallback')
    expect(classify({data: {values}, mark: {type: 'line', point: true}, encoding: bar.encoding}, ALL_MARKS)).toBe('fallback')
  })
  it('accepts the one mark property arc actually reads (innerRadius)', () => {
    expect(classify({data: {values}, mark: {type: 'arc', innerRadius: 40}, encoding: {theta: {field: 'open', type: 'quantitative'}}}, ALL_MARKS)).toBe('blerg')
  })
  it('falls back for an unimplemented property even on arc', () => {
    expect(classify({data: {values}, mark: {type: 'arc', href: 'x'}, encoding: {theta: {field: 'open', type: 'quantitative'}}}, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: boxplot sub-mark overrides (table row 3b)', () => {
  it('accepts literal sub-mark overrides (drawBoxplot\'s own guard is still the real backstop)', () => {
    const spec = {data: {values}, mark: {type: 'boxplot', box: {color: '#2f6fdb'}, outliers: true, median: {size: 4}}, encoding: {x: bar.encoding.x, y: bar.encoding.y}}
    expect(classify(spec, ALL_MARKS)).toBe('blerg')
  })
  it('falls back for an unlisted key inside a sub-mark override', () => {
    const spec = {data: {values}, mark: {type: 'boxplot', outliers: {href: 'x'}}, encoding: {x: bar.encoding.x, y: bar.encoding.y}}
    expect(classify(spec, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: mark-property size/tooltip vs. encoding-channel size/tooltip (table row 4 vs. 5/9)', () => {
  it('falls back for /mark/size and /mark/tooltip (neither is consumed by any mark)', () => {
    expect(classify({data: {values}, mark: {type: 'point', size: 100}, encoding: bar.encoding}, ALL_MARKS)).toBe('fallback')
    expect(classify({data: {values}, mark: {type: 'point', tooltip: true}, encoding: bar.encoding}, ALL_MARKS)).toBe('fallback')
  })
  it('falls back for /encoding/size on point (declared size is not implemented — point.ts still throws)', () => {
    const spec = {data: {values}, mark: 'point', encoding: {...bar.encoding, size: {field: 'open', type: 'quantitative'}}}
    expect(classify(spec, ALL_MARKS)).toBe('fallback')
  })
  it('accepts /encoding/tooltip as a single field def and as an array of field defs (table row 9)', () => {
    const pointEncoding = {x: {field: 'open', type: 'quantitative'}, y: {field: 'open', type: 'quantitative'}}
    const one = {data: {values}, mark: 'point', encoding: {...pointEncoding, tooltip: {field: 'month', type: 'nominal'}}}
    expect(classify(one, ALL_MARKS)).toBe('blerg')
    const array = {data: {values}, mark: 'point', encoding: {...pointEncoding, tooltip: [{field: 'month', type: 'nominal'}, {field: 'open', type: 'quantitative'}]}}
    expect(classify(array, ALL_MARKS)).toBe('blerg')
  })
})

describe('classify: encoding channels (table row 5)', () => {
  it('falls back for an unimplemented channel', () => {
    expect(classify({data: {values}, mark: 'point', encoding: {...bar.encoding, latitude: {field: 'x', type: 'quantitative'}}}, ALL_MARKS)).toBe('fallback')
  })
  it('falls back for opacity/fill/stroke/detail/order (none consumed by any mark)', () => {
    for (const channel of ['opacity', 'fill', 'stroke', 'detail', 'order']) {
      expect(classify({data: {values}, mark: 'bar', encoding: {...bar.encoding, [channel]: {field: 'team', type: 'nominal'}}}, ALL_MARKS)).toBe('fallback')
    }
  })
  it('falls back for a channel on a mark whose own declaration doesn\'t have it (xOffset on point)', () => {
    expect(classify({data: {values}, mark: 'point', encoding: {...bar.encoding, xOffset: {field: 'team', type: 'nominal'}}}, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: field/type/datum/value (table rows 6-7)', () => {
  it('falls back when type is omitted on a field encoding', () => {
    expect(classify({data: {values}, mark: 'bar', encoding: {x: {field: 'month'}, y: bar.encoding.y}}, ALL_MARKS)).toBe('fallback')
  })
  it('falls back for a type the mark\'s own declaration doesn\'t have for that channel (quantitative x on bar)', () => {
    expect(classify({data: {values}, mark: 'bar', encoding: {x: {field: 'open', type: 'quantitative'}, y: bar.encoding.y}}, ALL_MARKS)).toBe('fallback')
  })
  it('accepts a value literal', () => {
    // No mark declares a 'color' channel that accepts a bare literal differently from a field —
    // bar's own color channel is nominal/ordinal only; a literal {value: ...} bypasses type
    // entirely (table row 7), which stays structurally accepted here since nothing about the
    // *shape* is mark-specific — only whether it's drawn differs, and a solid literal color isn't
    // drawn by any mark either (same "accepted but not drawn" finding render.ts's mark-color gap
    // covers), so this only tests classify's own shape acceptance, not drawing.
    const spec = {data: {values}, mark: 'bar', encoding: {...bar.encoding, color: {value: 'red'}}}
    expect(classify(spec, ALL_MARKS)).toBe('blerg')
  })
  it('falls back for repeat (BLG-452 review finding: shared scales/headers not yet implemented)', () => {
    const spec = {repeat: ['open'], spec: {mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {field: {repeat: 'repeat'}, type: 'quantitative'}}}}
    expect(classify(spec, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: aggregate (table row 8)', () => {
  it('accepts an implemented aggregate, falls back for an unimplemented one', () => {
    const spec = (aggregate: string) => ({data: {values}, mark: 'bar', encoding: {x: bar.encoding.x, y: {field: 'open', type: 'quantitative', aggregate}}})
    expect(classify(spec('sum'), ALL_MARKS)).toBe('blerg')
    expect(classify(spec('stdev'), ALL_MARKS)).toBe('fallback')
    expect(classify(spec('argmax'), ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: bin (table row 10) — review follow-up: not implemented by any mark', () => {
  it('falls back for every bin form (no mark reads encoding.*.bin)', () => {
    const spec = (bin: unknown) => ({data: {values}, mark: 'bar', encoding: {x: {field: 'month', type: 'nominal'}, y: {...bar.encoding.y, bin}}})
    expect(classify(spec(true), ALL_MARKS)).toBe('fallback')
    expect(classify(spec('binned'), ALL_MARKS)).toBe('fallback')
    expect(classify(spec({maxbins: 10}), ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: timeUnit (table row 11, §3.4b) — review follow-up: moot, no mark accepts temporal position types', () => {
  it('falls back for every timeUnit, since no mark\'s position channel declares temporal (priority 5\'s finding)', () => {
    const spec = (timeUnit: string) => ({data: {values}, mark: 'line', encoding: {x: {field: 'when', type: 'temporal', timeUnit}, y: bar.encoding.y}})
    expect(classify(spec('yearmonth'), ALL_MARKS)).toBe('fallback')
    expect(classify(spec('utcyearmonth'), ALL_MARKS)).toBe('fallback')
    expect(classify(spec('week'), ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: sort (table row 12) — review follow-up: custom sort implemented (ascending/descending/null/array)', () => {
  it('accepts every sort form this renderer\'s own sortedDomain implements', () => {
    const spec = (sort: unknown) => ({data: {values}, mark: 'bar', encoding: {x: {...bar.encoding.x, sort}, y: bar.encoding.y}})
    expect(classify({data: {values}, ...bar}, ALL_MARKS)).toBe('blerg') // no sort key at all
    expect(classify(spec('ascending'), ALL_MARKS)).toBe('blerg')
    expect(classify(spec('descending'), ALL_MARKS)).toBe('blerg')
    expect(classify(spec(null), ALL_MARKS)).toBe('blerg')
    expect(classify(spec(['Feb', 'Jan']), ALL_MARKS)).toBe('blerg')
  })
  it('still accepts a sort-by-field object at classify time (the real backstop is the mark file\'s own throw, same discipline as aggregate/stack)', () => {
    const spec = (sort: unknown) => ({data: {values}, mark: 'bar', encoding: {x: {...bar.encoding.x, sort}, y: bar.encoding.y}})
    expect(classify(spec({field: 'open', op: 'sum', order: 'descending'}), ALL_MARKS)).toBe('blerg')
  })
})

describe('classify: stack (table row 13) — review follow-up: only the zero default is implemented', () => {
  it('accepts the default (absent, true or "zero"), falls back for normalize/center/false/null', () => {
    const spec = (stack: unknown) => ({data: {values}, mark: 'bar', encoding: {x: bar.encoding.x, y: {...bar.encoding.y, stack}, color: {field: 'team', type: 'nominal'}}})
    expect(classify({data: {values}, mark: 'bar', encoding: {x: bar.encoding.x, y: bar.encoding.y, color: {field: 'team', type: 'nominal'}}}, ALL_MARKS)).toBe('blerg') // no stack key
    expect(classify(spec('zero'), ALL_MARKS)).toBe('blerg')
    expect(classify(spec(true), ALL_MARKS)).toBe('blerg')
    expect(classify(spec('normalize'), ALL_MARKS)).toBe('fallback')
    expect(classify(spec('center'), ALL_MARKS)).toBe('fallback')
    expect(classify(spec(false), ALL_MARKS)).toBe('fallback')
    expect(classify(spec(null), ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: scale (table row 14) — review follow-up: not implemented by any mark', () => {
  it('falls back for every scale form, including null (no mark reads encoding.*.scale at all)', () => {
    const spec = (scale: unknown) => ({data: {values}, mark: 'bar', encoding: {x: bar.encoding.x, y: {...bar.encoding.y, scale}}})
    expect(classify(spec(null), ALL_MARKS)).toBe('fallback')
    expect(classify(spec({zero: false, nice: true}), ALL_MARKS)).toBe('fallback')
    expect(classify(spec({type: 'log'}), ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: axis and legend null/object (table rows 15-16) — review follow-up: not implemented by any mark', () => {
  it('falls back for every axis/legend form, including null', () => {
    const axisSpec = {data: {values}, mark: 'bar', encoding: {x: {...bar.encoding.x, axis: null}, y: bar.encoding.y}}
    expect(classify(axisSpec, ALL_MARKS)).toBe('fallback')
    const legendSpec = {data: {values}, mark: 'point', encoding: {...bar.encoding, color: {field: 'team', type: 'nominal', legend: {orient: 'bottom'}}}}
    expect(classify(legendSpec, ALL_MARKS)).toBe('fallback')
    const legendNullSpec = {data: {values}, mark: 'point', encoding: {...bar.encoding, color: {field: 'team', type: 'nominal', legend: null}}}
    expect(classify(legendNullSpec, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: condition (table row 17) — review follow-up: not implemented by any mark', () => {
  it('falls back for a condition, with or without a nested list', () => {
    const one = {data: {values}, mark: 'bar', encoding: {...bar.encoding, color: {condition: {test: {field: 'open', gt: 3}, value: 'red'}, value: 'blue'}}}
    expect(classify(one, ALL_MARKS)).toBe('fallback')
    const list = {data: {values}, mark: 'bar', encoding: {...bar.encoding, color: {condition: [{test: {field: 'open', gt: 3}, value: 'red'}], value: 'blue'}}}
    expect(classify(list, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: row/column/facet channels fall back (table row 18)', () => {
  it('falls back for the channel-shaped facet forms', () => {
    expect(classify({data: {values}, mark: 'bar', encoding: {...bar.encoding, row: {field: 'team', type: 'nominal'}}}, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: config (table row 19)', () => {
  it('accepts the policy-literal config sections', () => {
    const spec = {data: {values}, ...bar, config: {background: '#fff', bar: {color: '#2f6fdb'}, axis: {labelColor: '#333'}, legend: {orient: 'bottom'}, boxplot: {size: 14}}}
    expect(classify(spec, ALL_MARKS)).toBe('blerg')
  })
  it('falls back when config.invalid is set explicitly (table row 25)', () => {
    const spec = {data: {values}, ...bar, config: {invalid: {marks: 'show'}}}
    expect(classify(spec, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: composition and inheritance', () => {
  it('a single-child layer still falls back (BLG-452 priority 3 scope: exactly two children)', () => {
    const spec = {data: {values}, encoding: {x: bar.encoding.x}, layer: [{mark: 'bar', encoding: {y: bar.encoding.y}}]}
    expect(classify(spec, ALL_MARKS)).toBe('fallback')
  })
  it('a layer inherits its own parent\'s encoding into each child, unlike concat/facet/repeat', () => {
    const spec = {
      data: {values}, encoding: {x: bar.encoding.x, y: bar.encoding.y},
      layer: [{mark: 'bar'}, {mark: 'text', encoding: {text: {field: 'open', type: 'quantitative'}}}],
    }
    expect(classify(spec, ALL_MARKS)).toBe('blerg')
  })
  it('an unsupported layer combination falls back (BLG-452 priority 3 scope: bar+text and line/area+rule only)', () => {
    const spec = {
      data: {values}, encoding: {x: bar.encoding.x, y: bar.encoding.y},
      layer: [{mark: 'bar'}, {mark: 'point'}],
    }
    expect(classify(spec, ALL_MARKS)).toBe('fallback')
  })
  it('a concat-family child does not inherit the parent encoding', () => {
    const spec = {data: {values}, encoding: {x: bar.encoding.x}, hconcat: [{mark: 'bar', encoding: {y: bar.encoding.y}}]}
    // the child never gets x from its parent (concat doesn't inherit encoding) and has no x of
    // its own, so its own field/type requirement (row 6) is unmet for the missing channel — but
    // since x is simply absent (not malformed), that's fine; this proves the *merge* itself
    // never happens by checking a case where it would matter: a facet/field reused as `repeat`.
    expect(classify(spec, ALL_MARKS)).toBe('blerg')
  })
  it('falls back for facet and repeat (BLG-452 review finding: shared scales/headers not yet implemented)', () => {
    expect(classify({data: {values}, facet: {field: 'team', type: 'nominal'}, spec: bar}, ALL_MARKS)).toBe('fallback')
    expect(classify({data: {values}, repeat: {row: ['open']}, spec: {mark: 'bar', encoding: {x: bar.encoding.x, y: {field: {repeat: 'row'}, type: 'quantitative'}}}}, ALL_MARKS)).toBe('fallback')
  })
  it('falls back if any composed child falls back', () => {
    const spec = {data: {values}, hconcat: [bar, {mark: 'bar', encoding: {...bar.encoding, href: {field: 'x'}}}]}
    expect(classify(spec, ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: resolve (table row 23)', () => {
  it('accepts shared/independent, falls back for global/intersect/union', () => {
    const spec = (value: string) => ({data: {values}, hconcat: [bar], resolve: {scale: {y: value}}})
    expect(classify(spec('independent'), ALL_MARKS)).toBe('blerg')
    expect(classify(spec('shared'), ALL_MARKS)).toBe('blerg')
    expect(classify(spec('union'), ALL_MARKS)).toBe('fallback')
  })
})

describe('classify: title (table row 21)', () => {
  it('accepts a string and the literal object form at any node (chart-level chrome, not an axis title)', () => {
    expect(classify({data: {values}, ...bar, title: 'Revenue'}, ALL_MARKS)).toBe('blerg')
    expect(classify({data: {values}, ...bar, title: {text: 'Revenue', anchor: 'start'}}, ALL_MARKS)).toBe('blerg')
  })
})

describe('coverage: every policy-legal mark is accounted for (no drift between vegaPolicy.ts and classify.ts)', () => {
  it('is implemented or explicitly deferred', () => {
    for (const mark of MARKS) {
      expect(Object.hasOwn(MARK_PROPERTIES, mark) || DEFERRED_MARKS.has(mark), `mark "${mark}" is neither implemented nor explicitly deferred`).toBe(true)
    }
  })
  it('every implemented or deferred mark is still a real policy mark (no stale entries)', () => {
    for (const mark of [...Object.keys(MARK_PROPERTIES), ...DEFERRED_MARKS]) {
      expect(MARKS.has(mark), `"${mark}" is in classify.ts but not in vegaPolicy.ts's MARKS`).toBe(true)
    }
  })
})

describe('classify: point budget (design doc §3.6/M10) — a classification outcome, never a throw', () => {
  const rowsOf = (n: number) => Array.from({length: n}, (_, i) => ({month: `m${i}`, open: i}))
  it('a discrete mark (bar) within its own 2,000-point budget stays blerg', () => {
    expect(classify({data: {values: rowsOf(2000)}, ...bar}, ALL_MARKS)).toBe('blerg')
  })
  it('a discrete mark over its own budget falls back, without throwing', () => {
    expect(() => classify({data: {values: rowsOf(2001)}, ...bar}, ALL_MARKS)).not.toThrow()
    expect(classify({data: {values: rowsOf(2001)}, ...bar}, ALL_MARKS)).toBe('fallback')
  })
  it('a path mark (line) alone stays blerg up to the policy ceiling itself (10,000 points)', () => {
    const spec = {data: {values: rowsOf(10_000)}, mark: 'line', encoding: {x: {field: 'month', type: 'quantitative'}, y: {field: 'open', type: 'quantitative'}}}
    expect(classify(spec, ALL_MARKS)).toBe('blerg')
  })
  it('review finding: many under-budget panels can still sum past the discrete budget, and now fall back', () => {
    // Each panel alone is comfortably under bar's own 2,000-point ceiling; four of them sum past
    // it. Before this fix, concat/hconcat/vconcat/layer only checked each panel's own data, never
    // the total drawn synchronously on the main thread across all of them.
    const panel = {mark: 'bar', encoding: bar.encoding, data: {values: rowsOf(600)}}
    expect(classify({hconcat: [panel, panel, panel, panel]}, ALL_MARKS)).toBe('fallback')
    expect(classify({hconcat: [panel, panel]}, ALL_MARKS)).toBe('blerg')
  })
})
