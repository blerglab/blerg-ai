// Agent output is untrusted. A Vega-Lite spec is checked here, before the renderer is imported.
// What may draw: inline data, marks from a fixed list, composition (layer, concat, facet, repeat)
// and theming through an allowlisted `config`. What may not: data transforms (not yet), anything
// that loads, links, evaluates an expression, binds to the page or reacts to input, and any string
// where Vega-Lite's schema takes a number or a fixed word (Vega-Lite writes some of those values
// straight into expression source). Every refusal names the key and where it is.

import { NUMBER_ONLY, STRING_ENUMS } from './vegaLiteStrings.generated'

/** Why a chart is refused: `Chart refused: "expr" at $.encoding.x (expressions are disabled)`. */
export class ChartRefused extends Error {}

export const MAX_CHART_SOURCE = 131_072
const MAX_NODES = 30_000
const MAX_DEPTH = 40
const MAX_ARRAY = 10_000
const MAX_MARKS = 32
const MAX_PANELS = 16
// Panels a whole chart may expand to: facet cells x repeats x concatenated charts.
const MAX_TOTAL_PANELS = 64
const MAX_LIST = 64
// Points Vega-Lite may add when it fills in a stacked area (or stacked line/trail): keys x groups.
const MAX_IMPUTED = 100_000
// The largest number a d3 format string may name (a width or precision).
const MAX_FORMAT_NUMBER = 100
// d3-format's specifier pattern (d3-format/src/formatSpecifier.js): [[fill]align][sign][symbol][0][width][,][.precision][~][type].
const D3_FORMAT_SPECIFIER = /^(?:(.)?([<>=^]))?([+\-( ])?([$#])?(0)?(\d+)?(,)?(\.\d+)?(~)?([a-z%])?$/i
const PATH_MARKS = new Set(['area', 'line', 'trail'])
// An ISO 8601 date or date-time: without a timeUnit Vega-Lite quotes it as a string literal.
const ISO_DATE = /^\d{4}(?:-\d{2}(?:-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?)?)?$/
const GROUP_CHANNELS = ['color', 'fill', 'stroke', 'detail', 'opacity', 'fillOpacity', 'strokeOpacity', 'size', 'strokeWidth', 'strokeDash', 'shape']
// Keys whose string value names a data field.
const FIELD_KEYS = new Set(['field', 'argmin', 'argmax'])

const MARKS = new Set(['bar', 'line', 'point', 'circle', 'square', 'rect', 'area', 'tick', 'rule', 'text', 'trail', 'arc'])
const PROTO = new Set(['__proto__', 'prototype', 'constructor'])
const COMPOSE = new Set(['layer', 'concat', 'hconcat', 'vconcat'])
const COMPOUND = ['concat', 'hconcat', 'vconcat', 'facet', 'repeat']

const EXPRESSIONS = 'expressions are disabled'
const INTERACTION = 'parameters and interaction are disabled'
const RESOURCES = 'external resources are disabled'
// Refused wherever they appear outside inline data (compared lowercase).
const REFUSED = new Map<string, string>([
  ['url', RESOURCES], ['href', 'links are disabled'], ['$ref', 'external references are disabled'],
  ['expr', EXPRESSIONS], ['signal', 'signals are disabled'], ['calculate', EXPRESSIONS],
  ['params', INTERACTION], ['param', INTERACTION], ['bind', INTERACTION], ['selection', INTERACTION],
  ['customformattypes', 'custom formatters are disabled'],
  ['transform', 'data transforms are not available yet'],
  // Imputation compiles its key values into sequence(…) and its frame into the dataflow.
  ['impute', 'imputation is not available'],
])
// Where the schema allows a free string for some uses of a key but these words only for this one.
const STRING_OVERRIDES: Record<string, readonly string[]> = {
  // Encoding stack (the stack transform, which took a field name, is refused).
  stack: ['zero', 'center', 'normalize'],
}
// Allowed only where the spec structure puts them; anywhere else they are refused.
const PLACED = new Set(['config', 'datasets', 'layer', 'concat', 'hconcat', 'vconcat', 'spec', 'facet', 'repeat', 'mark', 'data'])

const TIME_UNIT = /^(?:utc)?(?:year|quarter|month|week|day|dayofyear|date|hours|minutes|seconds|milliseconds)+$/i
const SCHEMA = /^https:\/\/vega\.github\.io\/schema\/vega-lite\/v\d+(?:\.\d+)*\.json$/
// Font families by name only: no url(), quotes are fine, no other punctuation.
const FONT = /^[\p{L}\p{N} _'",-]{1,200}$/u
// What a literal config string may never contain.
const UNSAFE_STRING = /(?:https?|ftp|data|file|blob|javascript|vbscript):|\/\/|url\s*\(|image-set\s*\(|expression\s*\(|@import|[<\\]/i

const AXIS_CONFIG = /^axis(?:[XY]?(?:Band|Discrete|Point|Quantitative|Temporal)?|Top|Bottom|Left|Right)$/
const LITERAL_CONFIG = new Set(['legend', 'header', 'headerColumn', 'headerRow', 'headerFacet', 'title', 'view'])
const RANGE_NAMES = new Set(['category', 'diverging', 'heatmap', 'ordinal', 'ramp', 'symbol'])
const AUTOSIZE_TYPES = new Set(['pad', 'fit', 'fit-x', 'fit-y', 'none'])
const VIEW_SIZES = new Set(['continuousWidth', 'continuousHeight', 'discreteWidth', 'discreteHeight'])

function show(key: string): string {
  return key.length > 40 ? key.slice(0, 39) + '…' : key
}

function at(path: string, key: string | number): string {
  if (typeof key === 'number') return `${path}[${key}]`
  return /^[A-Za-z_$][\w$]*$/.test(key) ? `${path}.${show(key)}` : `${path}[${JSON.stringify(show(key))}]`
}

function refuse(path: string, why: string, key?: string | number): never {
  const where = path.length > 120 ? path.slice(0, 119) + '…' : path
  throw new ChartRefused(key === undefined ? `Chart refused at ${where}: ${why}` : typeof key === 'number'
    ? `Chart refused at ${at(where, key)}: ${why}` : `Chart refused: "${show(key)}" at ${where} (${why})`)
}

const isObject = (value: unknown): value is Record<string, unknown> => !!value && typeof value === 'object' && !Array.isArray(value)
const isLiteral = (value: unknown) => typeof value === 'string' || (typeof value === 'number' && Number.isFinite(value)) || typeof value === 'boolean' || value === null

// A Vega-Lite DateTime. Vega-Lite writes its components straight into generated expression
// source (`datetime(<year>, <month>, …)`), so each one must be a bounded integer or, for month and
// day, an English name; never any other string.
const DATE_TIME_PARTS: Record<string, [number, number]> = {
  year: [0, 9999], quarter: [1, 4], month: [1, 12], date: [1, 31], day: [0, 6],
  hours: [0, 23], minutes: [0, 59], seconds: [0, 59], milliseconds: [0, 999],
}
const MONTH_NAME = /^(?:jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sep(?:tember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?)$/i
const DAY_NAME = /^(?:sun(?:day)?|mon(?:day)?|tue(?:sday)?|wed(?:nesday)?|thu(?:rsday)?|fri(?:day)?|sat(?:urday)?)$/i
const isDateTimeLike = (value: Record<string, unknown>) => Object.keys(value).some(key => key === 'utc' || Object.hasOwn(DATE_TIME_PARTS, key))

/** Checks an object that carries any DateTime key as a DateTime, strictly. */
function checkDateTime(value: Record<string, unknown>, path: string) {
  for (const [key, item] of Object.entries(value)) {
    if (key === 'utc') {
      if (typeof item !== 'boolean') refuse(path, 'a date takes utc as true or false', key)
      continue
    }
    const range = Object.hasOwn(DATE_TIME_PARTS, key) ? DATE_TIME_PARTS[key] : undefined
    if (!range) refuse(path, 'a date takes only year, quarter, month, date, day, hours, minutes, seconds, milliseconds and utc', key)
    if (typeof item === 'string' && ((key === 'month' && MONTH_NAME.test(item)) || (key === 'day' && DAY_NAME.test(item)))) continue
    if (!Number.isInteger(item) || (item as number) < range[0] || (item as number) > range[1]) {
      refuse(path, `must be a whole number from ${range[0]} to ${range[1]}${key === 'month' ? ' or a month name' : key === 'day' ? ' or a weekday name' : ''}`, key)
    }
  }
}

/** A field reference whose path reaches a prototype: Vega's aggregation writes through such a
 *  path onto Object.prototype. Escapes (a\\.b) are dropped first, so every segment is seen. */
function isPrototypeField(name: string): boolean {
  return name.replace(/\\/g, '').split(/[.[\]'"]/).some(part => PROTO.has(part.trim()))
}

/** Parses and checks a Vega-Lite spec; returns the spec to draw, with bounded outer size. */
export function parseVegaLite(source: string): Record<string, unknown> {
  if (!source.trim()) throw new ChartRefused('Chart refused: the chart is empty')
  if (source.length > MAX_CHART_SOURCE) throw new ChartRefused(`Chart refused: the chart is too large (${source.length} characters; the limit is ${MAX_CHART_SOURCE})`)
  const root: unknown = JSON.parse(source)
  if (!isObject(root)) throw new ChartRefused('Chart refused: a chart must be a JSON object')

  let nodes = 0
  let marks = 0
  const datasets = new Map<string, unknown[]>()
  // Pre-scan (before the checks, so bounded and fail-closed): orientation set in config makes the
  // stacked channel unproven.
  const config = isObject(root.config) ? root.config : {}
  const orientConfigured = ['mark', 'area', 'line', 'trail'].some(key => isObject(config[key]) && 'orient' in (config[key] as object))
    || (isObject(config.style) && Object.values(config.style).some(style => !isObject(style) || 'orient' in style))

  function tick(path: string, depth: number) {
    if (++nodes > MAX_NODES) refuse(path, `the chart is too complex (more than ${MAX_NODES} values)`)
    if (depth > MAX_DEPTH) refuse(path, `the chart is nested too deeply (more than ${MAX_DEPTH} levels)`)
  }

  /** Checks applied to every key outside inline data. */
  function checkKey(key: string, value: unknown, path: string) {
    if (PROTO.has(key)) refuse(path, 'reserved object key', key)
    const why = REFUSED.get(key.toLowerCase())
    if (why) refuse(path, why, key)
    if (/expr$/i.test(key)) refuse(path, EXPRESSIONS, key)
    if (/signal$/i.test(key)) refuse(path, 'signals are disabled', key)
    if (key.toLowerCase() === 'locale') refuse(path, 'locales are disabled', key)
    if (/font$/i.test(key) && (typeof value !== 'string' || !FONT.test(value))) refuse(path, 'fonts are plain family names', key)
    if (key === 'formatType' && !(typeof value === 'string' && ['number', 'time', 'utc'].includes(value))) refuse(path, 'custom formatters are disabled', key)
    // Vega-Lite interpolates bandPosition into generated formulas.
    if (key === 'bandPosition' && !(typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= 1)) refuse(path, 'must be a number from 0 to 1', key)
    if (FIELD_KEYS.has(key) && typeof value === 'string' && isPrototypeField(value)) refuse(path, 'reserved field name', key)
    checkSchemaStrings(key, value, path)
  }

  /** A number format may not name a width or precision above 100 ("100000000.0f" builds a huge
   *  string). Whatever the field or formatType, a string is a number format exactly when d3-format
   *  can parse it as a specifier (its own pattern, below); one that can't ("%Y", "%b 2026") is never
   *  formatted as a number, so only parseable specifiers are capped. */
  function checkFormat(key: string, value: unknown, path: string) {
    if (!/(?:^f|F)ormat$/.test(key) || typeof value !== 'string') return
    const spec = D3_FORMAT_SPECIFIER.exec(value)
    if (!spec) return
    const width = spec[6] ? Number(spec[6]) : 0
    const precision = spec[8] ? Number(spec[8].slice(1)) : 0
    if (width > MAX_FORMAT_NUMBER || precision > MAX_FORMAT_NUMBER) refuse(path, `format widths and precisions must be at most ${MAX_FORMAT_NUMBER}`, key)
  }

  /** A string where the Vega-Lite schema takes a number, or one of a few words, is refused. */
  function checkSchemaStrings(key: string, value: unknown, path: string) {
    const strings = typeof value === 'string' ? [value] : Array.isArray(value) ? value.filter(item => typeof item === 'string') : []
    if (!strings.length) return
    const words = Object.hasOwn(STRING_OVERRIDES, key) ? STRING_OVERRIDES[key] : Object.hasOwn(STRING_ENUMS, key) ? STRING_ENUMS[key] : NUMBER_ONLY.has(key) ? [] : undefined
    if (!words) return
    for (const item of strings) {
      if (!words.includes(item)) refuse(path, words.length ? `"${show(item)}" is not one of its allowed values` : 'must be a number, not text', key)
    }
  }

  /** Inline data: inert values, still bounded and free of prototype keys. */
  function inline(value: unknown, path: string, depth: number) {
    tick(path, depth)
    if (typeof value === 'number' && !Number.isFinite(value)) refuse(path, 'numbers must be finite')
    if (Array.isArray(value)) {
      if (value.length > MAX_ARRAY) refuse(path, `too many values (the limit is ${MAX_ARRAY})`)
      value.forEach((item, i) => inline(item, at(path, i), depth + 1))
    } else if (isObject(value)) {
      for (const [key, item] of Object.entries(value)) {
        if (PROTO.has(key)) refuse(path, 'reserved object key', key)
        inline(item, at(path, key), depth + 1)
      }
    }
  }

  function checkData(value: unknown, path: string, depth: number) {
    tick(path, depth)
    if (!isObject(value)) refuse(path, 'charts need inline data.values', 'data')
    for (const [key, item] of Object.entries(value)) {
      checkKey(key, item, path)
      if (key === 'values') {
        if (!Array.isArray(item)) refuse(at(path, key), 'data.values must be an array')
        inline(item, at(path, key), depth + 1)
      } else if (key === 'name') {
        if (typeof item !== 'string' || !datasets.has(item)) refuse(path, 'data.name must name an entry in the top-level datasets', key)
      } else {
        refuse(path, 'charts need inline data.values', key)
      }
    }
    if (Object.keys(value).length !== 1) refuse(path, 'charts need inline data.values', 'data')
  }

  // ---- Predicates (filter and condition tests): object form with literal operands only.
  function checkPredicate(value: unknown, path: string, depth: number, key: string | number) {
    tick(path, depth)
    if (typeof value === 'string') refuse(path, `${EXPRESSIONS}; use an object predicate such as {"field": "x", "gt": 0}`, key)
    if (!isObject(value)) refuse(path, 'a predicate must be an object', key)
    const here = at(path, key)
    if (isDateTimeLike(value)) checkDateTime(value, here)
    for (const [name, item] of Object.entries(value)) checkKey(name, item, here)
    if ('and' in value || 'or' in value) {
      const op = 'and' in value ? 'and' : 'or'
      const list = value[op]
      if (Object.keys(value).length !== 1 || !Array.isArray(list) || list.length > MAX_LIST) refuse(here, `"${op}" takes a list of predicates`, op)
      list.forEach((item, i) => checkPredicate(item, at(here, op), depth + 1, i))
      return
    }
    if ('not' in value) {
      if (Object.keys(value).length !== 1) refuse(here, '"not" takes one predicate', 'not')
      checkPredicate(value.not, here, depth + 1, 'not')
      return
    }
    if (typeof value.field !== 'string' || !value.field) refuse(here, 'a predicate needs a field name', 'field')
    // With a timeUnit, Vega-Lite turns a string operand into a DateTime component: numbers or
    // checked date objects only.
    const timed = 'timeUnit' in value
    const operand = (item: unknown, name: string, index?: number) => {
      const where = index === undefined ? here : at(here, name)
      const label = index === undefined ? name : index
      if (timed) return checkTimed(item, where, label)
      if (isObject(item)) return checkDateTime(item, at(where, label))
      if (!isLiteral(item)) refuse(where, 'predicate values must be literals', label)
    }
    let ops = 0
    for (const [name, item] of Object.entries(value)) {
      if (name === 'field') continue
      if (name === 'timeUnit') { checkTimeUnit(item, here, name); continue }
      if (['equal', 'lt', 'lte', 'gt', 'gte'].includes(name)) {
        operand(item, name)
      } else if (name === 'range') {
        if (!Array.isArray(item) || item.length !== 2) refuse(here, 'range takes two literal bounds', name)
        item.forEach((bound, i) => { if (bound !== null) operand(bound, name, i) })
      } else if (name === 'oneOf') {
        if (!Array.isArray(item) || item.length > 1000) refuse(here, 'oneOf takes a list of literals', name)
        item.forEach((choice, i) => operand(choice, name, i))
      } else if (name === 'valid') {
        if (typeof item !== 'boolean') refuse(here, 'valid takes true or false', name)
      } else {
        refuse(here, 'not a supported predicate', name)
      }
      ops++
    }
    if (ops !== 1) refuse(here, 'a predicate needs exactly one of equal, range, oneOf, lt, lte, gt, gte or valid', key)
  }

  function checkTimeUnit(value: unknown, path: string, key: string) {
    if (typeof value === 'string' && TIME_UNIT.test(value)) return
    if (isObject(value) && typeof value.unit === 'string' && TIME_UNIT.test(value.unit)
      && Object.entries(value).every(([k, v]) => k === 'unit' || ((k === 'utc' || k === 'binned') && typeof v === 'boolean'))) return
    refuse(path, 'timeUnit takes a unit name such as "yearmonth"', key)
  }

  // ---- Generic values (encoding, mark properties, titles, scales, axes, …).
  function checkValue(value: unknown, path: string, depth: number, parent: string) {
    tick(path, depth)
    if (typeof value === 'number' && !Number.isFinite(value)) refuse(path, 'numbers must be finite')
    if (Array.isArray(value)) {
      if (value.length > MAX_ARRAY) refuse(path, `too many values (the limit is ${MAX_ARRAY})`)
      value.forEach((item, i) => checkValue(item, at(path, i), depth + 1, parent))
      return
    }
    if (!isObject(value)) return
    if (isDateTimeLike(value)) return checkDateTime(value, path)
    if ('timeUnit' in value || value.type === 'temporal') {
      checkTimedValues(value, path)
    }
    for (const [key, child] of Object.entries(value)) {
      checkKey(key, child, path)
      checkFormat(key, child, path)
      if (key === 'filter' || key === 'test') { checkPredicate(child, path, depth + 1, key); continue }
      if (key === 'data') { checkData(child, at(path, key), depth + 1); continue }
      if (key === 'facet' && parent === 'encoding') { checkValue(child, at(path, key), depth + 1, key); continue }
      if (key === 'repeat' && parent === 'field' && typeof child === 'string') continue
      if (PLACED.has(key)) refuse(path, 'not allowed here', key)
      if (key === 'timeUnit') { checkTimeUnit(child, path, key); continue }
      checkBounds(key, child, path)
      checkValue(child, at(path, key), depth + 1, key)
    }
  }

  /** One value of a timed field (a number or a checked date): Vega-Lite turns a string here into
   *  a DateTime part and writes it into expression source. */
  function checkTimed(value: unknown, path: string, key: string | number, isoAllowed = false) {
    if (typeof value === 'number' && Number.isFinite(value)) return
    if (isObject(value)) return checkDateTime(value, at(path, key))
    // Without a timeUnit, Vega-Lite quotes a string here (datetime("2026-01-05")); an ISO date is
    // all a temporal field needs. With one, the string becomes a DateTime part: never a string.
    if (isoAllowed && typeof value === 'string' && ISO_DATE.test(value)) return
    refuse(path, isoAllowed ? 'a temporal field takes numbers, ISO dates such as "2026-01-05" or date objects' : 'with a timeUnit, use numbers or date objects such as {"year": 2026}', key)
  }

  /** A field with a timeUnit or a temporal type: every value Vega-Lite may turn into a date
   *  expression (scale domain, axis and legend values, sort order) goes through checkTimed. */
  function checkTimedValues(def: Record<string, unknown>, path: string) {
    const iso = !('timeUnit' in def)
    const each = (value: unknown, where: string, key: string) => {
      if (Array.isArray(value)) value.forEach((item, i) => { if (item !== null) checkTimed(item, at(where, key), i, iso) })
      else if (value !== null && value !== undefined) checkTimed(value, where, key, iso)
    }
    if (Array.isArray(def.sort)) each(def.sort, path, 'sort')
    if (isObject(def.scale)) {
      for (const [key, item] of Object.entries(def.scale)) if (key.startsWith('domain') && (key !== 'domain' || Array.isArray(item))) each(item, at(path, 'scale'), key)
    }
    for (const guide of ['axis', 'legend']) {
      const g = def[guide]
      if (isObject(g) && 'values' in g) each(g.values, at(path, guide), 'values')
    }
  }

  function checkBounds(key: string, value: unknown, path: string) {
    if (key === 'bin' && value !== true && value !== false && value !== 'binned') refuse(path, 'custom bins are disabled; use true or "binned"', key)
    if (key === 'bins' && (!Array.isArray(value) || value.length > 1000)) refuse(path, 'bins must be an explicit list of at most 1000 values', key)
    if (key === 'tickCount' && (typeof value !== 'number' || value < 1 || value > 1000)) refuse(path, 'tickCount must be a number from 1 to 1000', key)
    if (['maxbins', 'steps', 'maxsteps'].includes(key) && typeof value === 'number' && (value < 0 || value > 1000)) refuse(path, 'the count is too large (the limit is 1000)', key)
    if ((key === 'width' || key === 'height') && (typeof value !== 'number' || !Number.isFinite(value) || value < 1 || value > 1024)) refuse(path, 'width and height must be numbers from 1 to 1024', key)
  }

  // ---- Small checkers for config shapes.
  type Check = (value: unknown, path: string, key: string) => void
  const bool: Check = (value, path, key) => { if (typeof value !== 'boolean') refuse(path, 'must be true or false', key) }
  const num = (min: number, max: number): Check => (value, path, key) => {
    if (typeof value !== 'number' || !Number.isFinite(value) || value < min || value > max) refuse(path, `must be a number from ${min} to ${max}`, key)
  }
  const numPair: Check = (value, path, key) => {
    if (!Array.isArray(value) || value.length !== 2 || !value.every(v => typeof v === 'number' && Number.isFinite(v))) refuse(path, 'must be two numbers', key)
  }
  const oneOf = (allowed: Set<string> | string[]): Check => (value, path, key) => {
    if (typeof value !== 'string' || !(Array.isArray(allowed) ? allowed.includes(value) : allowed.has(value))) refuse(path, 'not a supported value', key)
  }

  function checkShape(value: unknown, path: string, shape: Record<string, Check>, required: string[]) {
    tick(path, 0)
    if (!isObject(value)) refuse(path, 'must be an object')
    if (isDateTimeLike(value)) checkDateTime(value, path)
    for (const [key, item] of Object.entries(value)) {
      checkKey(key, item, path)
      const check = shape[key]
      if (!check) refuse(path, 'not a supported setting here', key)
      check(item, path, key)
    }
    for (const key of required) if (!(key in value)) refuse(path, 'is required', key)
  }

  // ---- Config: theming only, from an allowlist, literal values, fail closed.
  function checkString(value: string, path: string, key: string) {
    if (value.length > 200 || UNSAFE_STRING.test(value)) refuse(path, 'config strings may not contain URLs or code', key)
  }

  function checkLiteral(value: unknown, path: string, depth: number, nested: Set<string>) {
    tick(path, depth)
    if (!isObject(value)) refuse(path, 'must be an object of literal settings')
    if (isDateTimeLike(value)) refuse(path, 'dates are not allowed in config', Object.keys(value).find(key => key === 'utc' || Object.hasOwn(DATE_TIME_PARTS, key)))
    for (const [key, item] of Object.entries(value)) {
      checkKey(key, item, path)
      checkFormat(key, item, path)
      // Guide values become expressions for timed fields; config has no use for them.
      if (key === 'values') refuse(path, 'not allowed in config', key)
      if (key === 'filter' || key === 'test' || key === 'condition') refuse(path, 'conditions are disabled in config', key)
      checkBounds(key, item, path)
      if (VIEW_SIZES.has(key)) num(1, 1024)(item, path, key)
      if (key === 'step') num(1, 200)(item, path, key)
      if (typeof item === 'string') checkString(item, path, key)
      else if (typeof item === 'number') { if (!Number.isFinite(item)) refuse(path, 'numbers must be finite', key) }
      else if (Array.isArray(item)) {
        if (item.length > MAX_LIST || !item.every(v => (typeof v === 'number' && Number.isFinite(v)) || typeof v === 'string')) refuse(path, `must be a list of at most ${MAX_LIST} literals`, key)
        item.forEach(v => typeof v === 'string' && checkString(v, path, key))
      } else if (isObject(item)) {
        // Name an expression or signal inside the object rather than the setting that holds it.
        for (const [inner, value] of Object.entries(item)) checkKey(inner, value, at(path, key))
        if (!nested.has(key)) refuse(path, 'only literal values are allowed in config', key)
        checkLiteral(item, at(path, key), depth + 1, new Set())
      } else if (typeof item !== 'boolean' && item !== null) refuse(path, 'only literal values are allowed in config', key)
    }
  }

  function checkRange(value: unknown, path: string, depth: number) {
    tick(path, depth)
    if (!isObject(value)) refuse(path, 'range must be an object of color or size lists', 'range')
    const here = at(path, 'range')
    for (const [key, item] of Object.entries(value)) {
      checkKey(key, item, here)
      if (!RANGE_NAMES.has(key)) refuse(here, 'not a supported range name', key)
      if (Array.isArray(item)) {
        if (item.length > MAX_LIST || !item.every(v => typeof v === 'string' || (typeof v === 'number' && Number.isFinite(v)))) refuse(here, `must be a list of at most ${MAX_LIST} colors or numbers`, key)
        item.forEach(v => typeof v === 'string' && checkString(v, here, key))
      } else if (isObject(item)) {
        checkShape(item, at(here, key), {
          scheme: (v, p, k) => { if (typeof v !== 'string' || !/^[a-z][a-z0-9-]{0,39}$/i.test(v)) refuse(p, 'must be a scheme name', k) },
          count: num(1, MAX_LIST), extent: numPair,
        }, ['scheme'])
      } else {
        refuse(here, 'must be a list of colors or a scheme', key)
      }
    }
  }

  function checkConfig(value: unknown, path: string, depth: number) {
    tick(path, depth)
    if (!isObject(value)) refuse(path, 'config must be an object', 'config')
    const here = at(path, 'config')
    for (const [key, item] of Object.entries(value)) {
      checkKey(key, item, here)
      if (key === 'background') {
        if (typeof item !== 'string') refuse(here, 'must be a color', key)
        checkString(item, here, key)
      } else if (key === 'font') {
        // checkKey already held it to a family name.
      } else if (key === 'padding') {
        const pad = num(0, 128)
        if (isObject(item)) checkShape(item, at(here, key), {top: pad, bottom: pad, left: pad, right: pad}, [])
        else pad(item, here, key)
      } else if (key === 'autosize') {
        if (typeof item === 'string') oneOf(AUTOSIZE_TYPES)(item, here, key)
        else checkShape(item, at(here, key), {type: oneOf(AUTOSIZE_TYPES), contains: oneOf(['content', 'padding']), resize: bool}, [])
      } else if (key === 'range') {
        checkRange(item, here, depth + 1)
      } else if (key === 'mark' || MARKS.has(key)) {
        checkLiteral(item, at(here, key), depth + 1, new Set(['point', 'line']))
      } else if (AXIS_CONFIG.test(key) || LITERAL_CONFIG.has(key)) {
        checkLiteral(item, at(here, key), depth + 1, new Set())
      } else if (key === 'style') {
        if (!isObject(item)) refuse(here, 'style must map names to literal settings', key)
        for (const [name, style] of Object.entries(item)) {
          checkKey(name, style, at(here, key))
          checkLiteral(style, at(at(here, key), name), depth + 2, new Set())
        }
      } else {
        refuse(here, 'not an allowed config setting', key)
      }
    }
  }

  // ---- Filled-in points: Vega-Lite imputes every key x group of a stacked path mark.
  const UNKNOWN = Symbol('unknown')
  /** The value at a field path, or UNKNOWN unless every segment is an own property of a plain
   *  object (an array, a missing key or a primitive on the way is not followed). */
  function fieldValue(row: unknown, field: string): unknown {
    let value: unknown = row
    for (const part of field.split('.')) {
      if (!isObject(value) || !Object.hasOwn(value, part)) return UNKNOWN
      value = value[part]
    }
    return value
  }
  /** Distinct combinations of these fields over the rows: an upper bound. A field that is not a
   *  plain name (a repeat reference, say) or does not resolve on every row counts every row. */
  function distinct(rows: unknown[], fields: Array<string | null>): number {
    if (fields.some(field => field === null)) return rows.length
    if (!fields.length) return 1
    const seen = new Set<string>()
    for (const row of rows) {
      const values = fields.map(field => fieldValue(row, field!))
      if (values.includes(UNKNOWN)) return rows.length
      seen.add(JSON.stringify(values.map(value => value ?? null)))
    }
    return seen.size
  }
  function fieldsOf(def: unknown): Array<string | null> {
    if (Array.isArray(def)) return def.flatMap(fieldsOf)
    if (!isObject(def)) return []
    // Escaped or bracketed paths are not followed here: they count as unknown (every row).
    const own = 'field' in def ? [typeof def.field === 'string' && !/[\\[\]]/.test(def.field) ? def.field : null] : []
    return [...own, ...(isObject(def.condition) ? fieldsOf(def.condition) : [])]
  }
  /** The channel Vega-Lite stacks (x or y), when its stack rules (stack.ts potentialStackedChannel)
   *  decide it without the mark's orientation; undefined in every other case. */
  function provenStackedChannel(mark: unknown, encoding: Record<string, unknown>): 'x' | 'y' | undefined {
    // Orientation set anywhere, offsets, ranges or a mark style can change the answer: not proven.
    if (orientConfigured || (isObject(mark) && ('orient' in mark || 'style' in mark))) return undefined
    if (['x2', 'y2', 'xOffset', 'yOffset'].some(channel => channel in encoding)) return undefined
    const plain = (channel: string) => {
      const def = encoding[channel]
      // A plain named field without bins: a repeat reference, a datum or a bin is not proven.
      return isObject(def) && typeof def.field === 'string' && !/[\\[\]]/.test(def.field) && !('bin' in def) && !('datum' in def) ? def : undefined
    }
    const x = plain('x')
    const y = plain('y')
    if (!x || !y) return undefined
    const quantitative = (def: Record<string, unknown>) => def.type === 'quantitative'
    const stackSet = (def: Record<string, unknown>) => def.stack !== undefined && def.stack !== null && def.stack !== false
    if (quantitative(x) && quantitative(y)) {
      if (stackSet(x)) return 'x'
      if (stackSet(y)) return 'y'
      const xAggregate = x.aggregate !== undefined
      const yAggregate = y.aggregate !== undefined
      if (xAggregate !== yAggregate) return xAggregate ? 'x' : 'y'
      return undefined // decided by orientation
    }
    // Exactly one unbinned quantitative channel is the stacked one, unless the other is unknown.
    const known = (def: Record<string, unknown>) => typeof def.type === 'string' && ['nominal', 'ordinal', 'temporal'].includes(def.type)
    if (quantitative(y) && known(x)) return 'y'
    if (quantitative(x) && known(y)) return 'x'
    return undefined
  }

  function checkImputation(mark: unknown, encoding: unknown, rows: unknown[], path: string) {
    const type = typeof mark === 'string' ? mark : isObject(mark) ? mark.type : undefined
    if (typeof type !== 'string' || !PATH_MARKS.has(type) || !isObject(encoding)) return
    const stacked = (channel: string) => {
      const def = encoding[channel]
      if (!isObject(def)) return false
      // Area stacks by default; line and trail only when asked.
      return type === 'area' ? def.stack !== null && def.stack !== false : def.stack !== undefined && def.stack !== null && def.stack !== false
    }
    const grouping = GROUP_CHANNELS.filter(channel => fieldsOf(encoding[channel]).length > 0)
    if (!grouping.length || (!stacked('x') && !stacked('y'))) return
    const groups = distinct(rows, grouping.flatMap(channel => fieldsOf(encoding[channel])))
    // The key is the positional channel the stack does not run along. Only when Vega-Lite's stack
    // rules prove which one that is do we count it alone; otherwise the larger of x and y.
    const measure = provenStackedChannel(mark, encoding)
    const keys = measure ? distinct(rows, fieldsOf(encoding[measure === 'y' ? 'x' : 'y']))
      : Math.max(distinct(rows, fieldsOf(encoding.x)), distinct(rows, fieldsOf(encoding.y)))
    if (keys * groups > MAX_IMPUTED) {
      refuse(at(path, 'encoding'), `too many points to fill in: ${keys.toLocaleString('en-US')} keys × ${groups.toLocaleString('en-US')} groups could make ${(keys * groups).toLocaleString('en-US')} points (the limit is ${MAX_IMPUTED.toLocaleString('en-US')})`, grouping[0])
    }
  }

  // ---- Specs: a unit (mark), a layer, a concatenation, a facet or a repeat.
  function repeatCount(value: unknown, path: string): number {
    const list = (item: unknown, key: string) => {
      if (!Array.isArray(item) || item.length > MAX_PANELS || !item.every(v => typeof v === 'string' && v.length <= 256)) refuse(path, `repeat takes at most ${MAX_PANELS} field names`, key)
      if (item.some(v => isPrototypeField(v))) refuse(path, 'reserved field name', key)
      return item.length
    }
    if (Array.isArray(value)) return list(value, 'repeat')
    if (!isObject(value)) refuse(path, 'repeat takes a list of field names', 'repeat')
    let panels = 1
    let layers = 1
    for (const [key, item] of Object.entries(value)) {
      checkKey(key, item, path)
      if (key === 'row' || key === 'column') panels *= list(item, key)
      else if (key === 'layer') layers = list(item, key)
      else refuse(at(path, 'repeat'), 'repeat takes row, column or layer', key)
    }
    if (panels > MAX_PANELS) refuse(path, `too many panels (the limit is ${MAX_PANELS})`, 'repeat')
    return panels * layers
  }

  /** Facet cells: distinct values of the facet fields (row x column, or the wrapped field). */
  function facetCells(facet: unknown, rows: unknown[]): number {
    if (!isObject(facet)) return 1
    if ('field' in facet || !('row' in facet || 'column' in facet)) return distinct(rows, fieldsOf(facet))
    return distinct(rows, fieldsOf(facet.row)) * distinct(rows, fieldsOf(facet.column))
  }

  /** Checks a spec and returns how many panels it expands to. */
  function checkSpec(spec: unknown, path: string, depth: number, top: boolean, copies: number, parentRows: unknown[], parentEncoding: Record<string, unknown>): number {
    tick(path, depth)
    if (!isObject(spec)) refuse(path, 'a chart must be a JSON object')
    // What this spec draws from: its own inline data or named dataset, else its parent's; a layer
    // also inherits its parent's encoding.
    const data = spec.data
    const rows = isObject(data) && Array.isArray(data.values) ? data.values
      : isObject(data) && typeof data.name === 'string' ? datasets.get(data.name) ?? [] : parentRows
    const encoding = {...parentEncoding, ...(isObject(spec.encoding) ? spec.encoding : {})}
    if (isDateTimeLike(spec)) checkDateTime(spec, path)
    let inner = 1
    if ('repeat' in spec) inner = repeatCount(spec.repeat, path)
    if ('spec' in spec && !('facet' in spec) && !('repeat' in spec)) refuse(path, '"spec" is allowed only inside a facet or repeat', 'spec')
    if (('facet' in spec || 'repeat' in spec) && !isObject(spec.spec)) refuse(path, 'a facet or repeat needs an inner "spec"', 'facet' in spec ? 'facet' : 'repeat')
    let panels = 1
    let composed = ''
    for (const [key, child] of Object.entries(spec)) {
      checkKey(key, child, path)
      const here = at(path, key)
      if (key === '$schema') {
        if (typeof child !== 'string' || !SCHEMA.test(child)) refuse(path, 'only the Vega-Lite schema URL is allowed', key)
      } else if (key === 'config') {
        if (!top) refuse(path, 'config is allowed only at the top of the chart', key)
        checkConfig(child, path, depth + 1)
      } else if (key === 'datasets') {
        if (!top) refuse(path, 'datasets are allowed only at the top of the chart', key)
        if (!isObject(child)) refuse(path, 'datasets must map names to inline arrays', key)
        for (const [name, values] of Object.entries(child)) {
          if (PROTO.has(name)) refuse(here, 'reserved object key', name)
          if (!Array.isArray(values)) refuse(here, 'a dataset must be an inline array', name)
          inline(values, at(here, name), depth + 2)
        }
      } else if (key === 'data') {
        checkData(child, here, depth + 1)
      } else if (key === 'mark') {
        marks += copies
        if (marks > MAX_MARKS) refuse(path, `too many marks (the limit is ${MAX_MARKS})`, key)
        const type = typeof child === 'string' ? child : isObject(child) ? child.type : undefined
        if (typeof type !== 'string' || !MARKS.has(type)) refuse(path, `not a supported mark; use one of ${[...MARKS].join(', ')}`, key)
        checkValue(child, here, depth + 1, key)
      } else if (COMPOSE.has(key)) {
        if (!Array.isArray(child) || child.length > MAX_PANELS) refuse(path, `must be a list of at most ${MAX_PANELS} charts`, key)
        const counts = child.map((item, i) => checkSpec(item, at(here, i), depth + 2, false, copies, rows, key === 'layer' ? encoding : {}))
        // Layers overlay in one panel; concatenated charts sit side by side.
        panels = key === 'layer' ? Math.max(1, ...counts) : counts.reduce((sum, n) => sum + n, 0)
        composed = key
      } else if (key === 'facet') {
        checkValue(child, here, depth + 1, key)
      } else if (key === 'repeat') {
        // Counted above.
      } else if (key === 'spec') {
        const cells = 'facet' in spec ? facetCells(spec.facet, rows) : repeatPanels(spec.repeat)
        panels = cells * checkSpec(child, here, depth + 1, false, copies * inner, rows, {})
        composed = 'facet' in spec ? 'facet' : 'repeat'
      } else {
        if (key === 'filter' || key === 'test') { checkPredicate(child, path, depth + 1, key); continue }
        if (PLACED.has(key)) refuse(path, 'not allowed here', key)
        checkBounds(key, child, path)
        checkValue(child, here, depth + 1, key)
      }
    }
    // After the keys are checked, so field names and data are known to be safe to read.
    if ('mark' in spec) {
      checkImputation(spec.mark, encoding, rows, path)
      // A unit can facet itself through its row, column and facet channels.
      panels = distinct(rows, fieldsOf(encoding.row)) * distinct(rows, fieldsOf(encoding.column)) * distinct(rows, fieldsOf(encoding.facet))
      composed = 'encoding'
    }
    if (panels > MAX_TOTAL_PANELS) refuse(path, `too many panels: ${panels.toLocaleString('en-US')} (the limit is ${MAX_TOTAL_PANELS})`, composed || undefined)
    return panels
  }

  /** Panels a repeat lays out (row x column, or the list); repeated layers overlay. */
  function repeatPanels(value: unknown): number {
    if (Array.isArray(value)) return value.length
    if (!isObject(value)) return 1
    return (Array.isArray(value.row) ? value.row.length : 1) * (Array.isArray(value.column) ? value.column.length : 1)
  }

  // Datasets first, so data.name can be checked against them wherever it appears.
  if (isObject(root.datasets)) {
    for (const [name, values] of Object.entries(root.datasets)) datasets.set(name, Array.isArray(values) ? values : [])
  }
  checkSpec(root, '$', 0, true, 1, [], {})
  if (marks === 0) throw new ChartRefused('Chart refused: the chart has no marks')

  const compound = isCompoundChart(root)
  // A single or layered plot gets fixed outer dimensions that fit the message, so categorical
  // step sizing cannot allocate an unbounded canvas. Compositions keep their authored panel sizes;
  // the renderer measures them before drawing and refuses one that would be too large.
  return {
    ...root,
    ...(compound ? {} : {width: 'width' in root ? root.width : 640, height: 'height' in root ? root.height : 360}),
    autosize: compound ? {type: 'pad'} : {type: 'fit', contains: 'padding'},
    padding: 16, background: 'transparent',
  }
}

/** A concatenation, facet or repeat: laid out from its panels, not fitted to one width. */
export function isCompoundChart(spec: Record<string, unknown>): boolean {
  return COMPOUND.some(key => key in spec)
}
