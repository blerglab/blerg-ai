import {expect, it} from 'vitest'
import {validDataset} from './datasets'
import {parseDashboard} from './dashboard'
import {parseVegaLite} from './vegaPolicy'
const card = {id: 'workers', kind: 'stat', title: 'Workers', span: 1, data: {kind: 'inline', rows: [{n: 0}]}, view: {value: 'n', selection: 'single'}}
const doc = (patch = {}) => JSON.stringify({format: 'blerg-dashboard', version: 1, title: 'Telemetry', cards: [card], ...patch})
it('accepts zero and unknown, refusing ambiguous selection', () => {
  expect((parseDashboard(doc()).cards[0].data as Extract<ReturnType<typeof parseDashboard>['cards'][number]['data'], {kind: 'inline'}>).rows[0].n).toBe(0)
  expect(() => parseDashboard(doc({cards: [{...card, data: {kind: 'inline', rows: [{n: null}]}}]}))).not.toThrow()
  expect(() => parseDashboard(doc({cards: [{...card, data: {kind: 'inline', rows: [{n: 1}, {n: 2}]}}]}))).toThrow(/single/)
})
it.each([{version: 0}, {cards: [card, card]}, {cards: [{...card, span: 5}]}, {color: 'red'}, {cards: [{...card, kind: 'future'}]}, {cards: [{...card, data: {kind: 'file', rows: []}}]}])('refuses invalid envelopes %j', patch => {
  expect(() => parseDashboard(doc(patch))).toThrow()
})
it('validates embedded charts and gauge ranges', () => {
  expect(() => parseDashboard(doc({cards: [{...card, kind: 'chart', view: undefined, spec: {mark: 'bar', data: {url: 'https://example.com'}}}]}))).toThrow()
  expect(() => parseDashboard(doc({cards: [{...card, kind: 'gauge', view: {...card.view, min: 10, max: 0}}]}))).toThrow()
})
it.each([{range: {category: ['red']}}, {background: 'red'}, {mark: {color: 'red'}}, {axis: {labelColor: 'red'}}, {range: {category: {scheme: 'blues'}}}])('refuses authored theme overrides %j', config => {
  expect(() => parseVegaLite(JSON.stringify({mark: 'bar', data: {values: []}, config}), true)).toThrow(/theme/)
})
it.each([
  {mark: {type: 'bar', color: 'red'}},
  {mark: 'bar', encoding: {color: {field: 'n', type: 'nominal', scale: {range: ['red']}}}},
  {mark: 'bar', config: {style: {cell: {fill: 'red'}}}},
  {layer: [{mark: {type: 'line', stroke: 'red'}}]},
])('refuses embedded colors at every spec level %j', spec => {
  expect(() => parseDashboard(doc({cards: [{...card, kind: 'chart', view: undefined, spec}]}))).toThrow(/theme/)
})
it('allows thematic series and ordinary inline data named color', () => {
  const spec = {mark: 'bar', encoding: {color: {field: 'color', type: 'nominal'}}}
  expect(() => parseDashboard(doc({cards: [{...card, kind: 'chart', view: undefined, spec, data: {kind: 'inline', rows: [{color: 'Group A'}]}}]}))).not.toThrow()
})
it('accepts only catalogued scalar datasets with bounded nonsecret parameters', () => {
  const live = {...card, data: {kind: 'dataset', name: 'blerg.workers', parameters: {}}}
  expect(() => parseDashboard(doc({cards: [live]}))).not.toThrow()
  for (const data of [
    {...live.data, name: 'sql'},
    {...live.data, parameters: {token: 'canary-private'}},
    {kind: 'dataset', name: 'blerg.cards.merged', parameters: {bucket: 'minute'}},
  ]) expect(() => parseDashboard(doc({cards: [{...live, data}]}))).toThrow()
  expect(() => parseDashboard(doc({cards: [{...live, kind: 'chart', view: undefined, spec: {mark: 'bar'}}]}))).toThrow()
})

it('refuses live sparkline and ambiguous merge row selection', () => {
  const data = {kind: 'dataset', name: 'blerg.cards.merged', parameters: {bucket: 'day', timezone: 'UTC'}}
  expect(() => parseDashboard(doc({cards: [{...card, data}]}))).toThrow(/latest selection/)
  expect(() => parseDashboard(doc({cards: [{...card, data, view: {...card.view, selection: 'latest', sparkline: 'count'}}]}))).toThrow(/unsupported-live/)
})

it('gate dataset accepts only its catalogued parameters', () => {
  expect(validDataset({name: 'blerg.gates.durations', parameters: {}})).toBe(true)
  expect(validDataset({name: 'blerg.gates.durations', parameters: {url: 'private-canary'}})).toBe(false)
})

it('validates card-time scope without accepting executable parameters', () => {
  expect(validDataset({name: 'blerg.cards.time', parameters: {board: 'board-a', card: '7'}})).toBe(true)
  expect(validDataset({name: 'blerg.cards.time', parameters: {board: 'board-a', card: '0'}})).toBe(false)
  expect(validDataset({name: 'blerg.cards.time', parameters: {board: 'https://private-canary', card: '7'}})).toBe(false)
})

it('accepts review pair scope with the same restrictions as card time', () => {
  expect(validDataset({name: 'blerg.reviews.pairs', parameters: {board: 'board-a', card: '7'}})).toBe(true)
  expect(validDataset({name: 'blerg.reviews.pairs', parameters: {board: 'board-a', card: '7', token: 'private-canary'}})).toBe(false)
})

it.each([
  ['blerg.gates.bins', {width: '60'}],
  ['blerg.cards.merged.heatmap', {bucket: 'hour', timezone: 'America/New_York'}],
  ['blerg.workers.timeline', {}],
])('accepts aggregate %s', (name, parameters) => {
  expect(validDataset({name, parameters})).toBe(true)
})
it.each(['0', '-1', '86401', '1e2', '60.5'])('refuses invalid bin width %s', width => {
  expect(validDataset({name: 'blerg.gates.bins', parameters: {width}})).toBe(false)
})

it('selects a gate suite without accepting a path', () => {
  expect(validDataset({name: 'blerg.gates.durations', parameters: {suite: 'total'}})).toBe(true)
  expect(validDataset({name: 'blerg.gates.durations', parameters: {suite: '/private-canary'}})).toBe(false)
})

it.each(['blerg.gates.durations', 'blerg.gates.bins', 'blerg.workers.timeline'])('validates the %s bounded time window', name => {
  const parameters = name === 'blerg.gates.bins' ? {width: '60', since: '2026-11-01T00:00:00Z'} : {since: '2026-11-01T00:00:00Z'}
  expect(validDataset({name, parameters})).toBe(true)
  expect(validDataset({name, parameters: {...parameters, since: 'not-a-timestamp'}})).toBe(false)
})
