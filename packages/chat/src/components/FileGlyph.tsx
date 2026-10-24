// One small glyph per view: a page with a mark that tells the kinds apart.
const GLYPHS: Record<string, string> = {
  markdown: 'M6 11V8l1.5 2L9 8v3',
  pdf: 'M6.5 12V8h2a1 1 0 0 1 0 2h-2',
  image: 'm5 12 2-3 1.5 2 1-1 2 2z',
  csv: 'M5.5 8.5h6M5.5 10.5h6M8.5 7v6',
  json: 'm7 8-2 2 2 2m3-4 2 2-2 2',
  diff: 'M8.5 7v3M7 8.5h3M7 12h3',
  html: 'M8.5 7a3 3 0 1 0 0 6 3 3 0 0 0 0-6',
  audio: 'M7 12V8.5l4-1V11',
  video: 'M7 8v4l3.5-2z',
  text: 'M6 8.5h5M6 10.5h5M6 12.5h3',
}

export function FileGlyph({ view, label }: { view: string; label: string }) {
  return (
    <svg className="artifact-glyph" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
      <title>{label}</title>
      <path d="M3.5 1.5h6L13 5v9.5H3.5zM9.5 1.5V5H13" fill="none" stroke="currentColor" strokeWidth="1" strokeLinejoin="round" />
      <path d={GLYPHS[view] ?? GLYPHS.text} fill="none" stroke="currentColor" strokeWidth="1" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  )
}

/** Folder counterpart with the same canvas, stroke and size as the Files list. */
export function FolderGlyph({ expanded = false }: { expanded?: boolean }) {
  return <svg className="artifact-glyph" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true"><path d={expanded ? 'M2 5V3h4l2 2h6v2M2 7h12l-2 6H2z' : 'M2 13V3h4l2 2h6v8z'} fill="none" stroke="currentColor" strokeWidth="1" strokeLinejoin="round" /></svg>
}
