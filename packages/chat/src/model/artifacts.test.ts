import { describe, expect, it } from 'vitest'
import {
  HTML_ARTIFACT_CSP,
  downloadFileName,
  fileCount,
  filenameFromDisposition,
  findLatest,
  formatSize,
  groupArtifacts,
  parseCsv,
  prettyJson,
  withCsp,
} from './artifacts'
import type { ArtifactInfo } from './artifacts'
import { blobTypeFor, isViewable, tooBigToPreview, ARTIFACT_VIEWS } from './artifactViews'

describe('formatSize', () => {
  it('writes sizes the way a person reads them', () => {
    expect(formatSize(0)).toBe('0 B')
    expect(formatSize(1023)).toBe('1023 B')
    expect(formatSize(1536)).toBe('1.5 KB')
    expect(formatSize(240 * 1024)).toBe('240 KB')
    expect(formatSize(5 * 1024 * 1024)).toBe('5.0 MB')
    expect(formatSize(-1)).toBe('')
  })
})

describe('parseCsv', () => {
  it('parses plain rows and ignores a trailing newline', () => {
    expect(parseCsv('a,b\n1,2\n').rows).toEqual([['a', 'b'], ['1', '2']])
    expect(parseCsv('a,b\n1,2').rows).toEqual([['a', 'b'], ['1', '2']])
  })
  it('handles quoted commas, newlines and escaped quotes', () => {
    const { rows } = parseCsv('name,note\n"Smith, J","line1\nline2"\n"say ""hi""",x\n')
    expect(rows).toEqual([['name', 'note'], ['Smith, J', 'line1\nline2'], ['say "hi"', 'x']])
  })
  it('accepts CRLF and lone CR line ends and a BOM', () => {
    expect(parseCsv('﻿a,b\r\n1,2\r\n3,4\r5,6').rows).toEqual([['a', 'b'], ['1', '2'], ['3', '4'], ['5', '6']])
  })
  it('keeps empty fields and a lone empty quoted field', () => {
    expect(parseCsv('a,,c\n,,\n').rows).toEqual([['a', '', 'c'], ['', '', '']])
    expect(parseCsv('""').rows).toEqual([['']])
    expect(parseCsv('').rows).toEqual([])
  })
  it('treats a quote in the middle of a field as a literal', () => {
    expect(parseCsv('5" pipe,x').rows).toEqual([['5" pipe', 'x']])
  })
  it('takes a tab as the delimiter for tsv', () => {
    expect(parseCsv('a\tb,c\n1\t2', '\t').rows).toEqual([['a', 'b,c'], ['1', '2']])
  })
  it('stops at the row cap and says so', () => {
    const text = Array.from({ length: 10 }, (_, i) => `r${i},x`).join('\n')
    const r = parseCsv(text, ',', 3)
    expect(r.rows.map(x => x[0])).toEqual(['r0', 'r1', 'r2'])
    expect(r.moreRows).toBe(true)
    expect(parseCsv(text, ',', 10).moreRows).toBe(false)
    expect(parseCsv(text + '\n', ',', 10).moreRows).toBe(false)
  })
  it('keeps at most the column cap and says so', () => {
    const r = parseCsv('a,b,c,d\n1,2,3,4', ',', 10, 2)
    expect(r.rows).toEqual([['a', 'b'], ['1', '2']])
    expect(r.moreCols).toBe(true)
    expect(parseCsv('a,b', ',', 10, 2).moreCols).toBe(false)
  })
  it('does not choke on an unterminated quote', () => {
    expect(parseCsv('a,"b\nc').rows).toEqual([['a', 'b\nc']])
  })
})

describe('prettyJson', () => {
  it('pretty-prints valid JSON', () => {
    expect(prettyJson('{"a":[1,2],"b":{"c":null}}')).toBe('{\n  "a": [\n    1,\n    2\n  ],\n  "b": {\n    "c": null\n  }\n}')
  })
  it('falls back to the raw text when it does not parse', () => {
    expect(prettyJson('{"a": ')).toBe('{"a": ')
    expect(prettyJson('{"a":1}\n{"a":2}')).toBe('{"a":1}\n{"a":2}') // json lines
  })
})

describe('withCsp', () => {
  const META = `<meta http-equiv="Content-Security-Policy" content="${HTML_ARTIFACT_CSP}">`
  it('carries the exact policy: no network, inline script and style, data: media', () => {
    expect(HTML_ARTIFACT_CSP).toBe(
      "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; script-src 'unsafe-inline'; font-src data:; media-src data: blob:; form-action 'none'; base-uri 'none'; frame-src 'none'",
    )
  })
  it('puts the meta at the very top of a fragment', () => {
    expect(withCsp('<h1>hi</h1><script>x()</script>')).toBe(META + '<h1>hi</h1><script>x()</script>')
  })
  it('keeps a leading doctype first (no quirks mode) and still precedes every script', () => {
    const out = withCsp('<!DOCTYPE html>\n<html><script>x()</script></html>')
    expect(out.startsWith('<!DOCTYPE html>' + META)).toBe(true)
    expect(out.indexOf('<meta')).toBeLessThan(out.indexOf('<script'))
  })
  it('does not let a script hide inside a fake doctype', () => {
    const out = withCsp('<!doctype html <script>alert(1)</script>')
    expect(out.indexOf('<meta')).toBeLessThan(out.indexOf('alert(1)'))
    const other = withCsp('<!doctype svg><script>x()</script>')
    expect(other.startsWith(META)).toBe(true)
  })
})

describe('the view table', () => {
  it('knows every view the server assigns, and nothing else is viewable', () => {
    for (const v of ['markdown', 'text', 'json', 'csv', 'image', 'pdf', 'audio', 'video', 'html']) expect(isViewable(v)).toBe(true)
    for (const v of ['none', '', 'hologram', 'constructor', '__proto__', 'toString']) expect(isViewable(v)).toBe(false)
  })
  it('gives blobs only allow-listed types, and a pdf is always a pdf', () => {
    expect(blobTypeFor('image', 'image/svg+xml')).toBe('image/svg+xml')
    expect(blobTypeFor('image', 'image/png; charset=x')).toBe('image/png')
    expect(blobTypeFor('image', 'text/html')).toBe('application/octet-stream')
    expect(blobTypeFor('pdf', 'text/html')).toBe('application/pdf')
    expect(blobTypeFor('audio', 'audio/mpeg')).toBe('audio/mpeg')
    expect(blobTypeFor('video', 'video/mp4')).toBe('video/mp4')
    expect(blobTypeFor('video', 'text/html')).toBe('application/octet-stream')
    expect(blobTypeFor('none', 'image/png')).toBe('application/octet-stream')
    expect(blobTypeFor('constructor', 'image/png')).toBe('application/octet-stream')
  })
  it('refuses to preview a huge textual file but not a big image', () => {
    expect(tooBigToPreview('text', 3 * 1024 * 1024)).toBe(true)
    expect(tooBigToPreview('text', 1024)).toBe(false)
    expect(tooBigToPreview('image', 20 * 1024 * 1024)).toBe(false)
    expect(Object.keys(ARTIFACT_VIEWS)).toContain('html')
  })
})

describe('versions', () => {
  const info = (id: string, name: string, version: number | undefined, over: Partial<ArtifactInfo> = {}): ArtifactInfo => ({
    id, name, size: 1, content_type: 'text/plain', view: 'text', version, created_at: `2026-09-30T1${version ?? 0}:00:00Z`, ...over,
  })

  it('downloadFileName: the newest version keeps the plain name, an older one gets -vN before the extension', () => {
    expect(downloadFileName({ name: 'report.md', version: 3, latest_version: 3 })).toBe('report.md')
    expect(downloadFileName({ name: 'report.md', version: 1, latest_version: 3 })).toBe('report-v1.md')
    expect(downloadFileName({ name: 'report.md', version: 2, latest_version: 3 })).toBe('report-v2.md')
    expect(downloadFileName({ name: 'LICENSE', version: 1, latest_version: 2 })).toBe('LICENSE-v1')
    expect(downloadFileName({ name: 'archive.tar.gz', version: 1, latest_version: 2 })).toBe('archive.tar-v1.gz')
    expect(downloadFileName({ name: '.env', version: 1, latest_version: 2 })).toBe('.env-v1')
    expect(downloadFileName({ name: 'x.md' })).toBe('x.md')
    expect(downloadFileName({ name: 'x.md', version: 1 })).toBe('x.md')
  })

  it('filenameFromDisposition reads the RFC 5987 name and refuses anything with a path', () => {
    expect(filenameFromDisposition("attachment; filename*=UTF-8''report-v1.md")).toBe('report-v1.md')
    expect(filenameFromDisposition("attachment; filename*=UTF-8''a%20b.txt")).toBe('a b.txt')
    expect(filenameFromDisposition("attachment; filename*=UTF-8''..%2F..%2Fevil.txt")).toBe('evil.txt')
    expect(filenameFromDisposition('attachment')).toBeNull()
    expect(filenameFromDisposition(null)).toBeNull()
    expect(filenameFromDisposition("attachment; filename*=UTF-8''%E0%A4%A")).toBeNull()
  })

  it('groupArtifacts: one group per (origin, name), the newest version first, in the order of the list', () => {
    const items = [
      info('c', 'report.md', 3),
      info('x', 'notes.md', 1),
      info('b', 'report.md', 2),
      info('u', 'report.md', 1, { origin: 'user' }),
      info('a', 'report.md', 1),
    ]
    const groups = groupArtifacts(items)
    expect(groups.map(g => [g.origin, g.name, g.latest.id, g.earlier.map(e => e.id)])).toEqual([
      ['agent', 'report.md', 'c', ['b', 'a']],
      ['agent', 'notes.md', 'x', []],
      ['user', 'report.md', 'u', []],
    ])
  })

  it('groupArtifacts: the latest is the highest version whatever the order, and files without a version stand alone', () => {
    const groups = groupArtifacts([info('a', 'r.md', 1), info('b', 'r.md', 2), info('p', 'old.txt', undefined), info('q', 'old.txt', undefined)])
    expect(groups.map(g => [g.name, g.latest.id, g.earlier.map(e => e.id)])).toEqual([
      ['r.md', 'b', ['a']],
      ['old.txt', 'p', []],
      ['old.txt', 'q', []],
    ])
  })

  it('fileCount counts files, not versions', () => {
    expect(fileCount([info('a', 'r.md', 1), info('b', 'r.md', 2), info('c', 'n.md', 1)])).toBe(2)
    expect(fileCount([])).toBe(0)
  })

  it('findLatest finds the newest version of a file from the list', () => {
    const items = [info('b', 'r.md', 2), info('a', 'r.md', 1), info('u', 'r.md', 1, { origin: 'user' })]
    expect(findLatest(items, items[1])?.id).toBe('b')
    expect(findLatest(items, items[2])?.id).toBe('u')
    expect(findLatest(items, info('z', 'gone.md', 1))).toBeUndefined()
  })
})
