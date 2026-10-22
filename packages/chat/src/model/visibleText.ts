// Hidden characters, shown: control characters (C0 but newline, tab and carriage return; DEL;
// C1), bidirectional and invisible formatting characters (marks, embeddings, overrides, isolates,
// zero-width characters, the word joiner, BOM, soft hyphen, Mongolian vowel separator, variation
// selectors), the line and paragraph separators and the tag characters (U+E0000 to U+E007F) become
// a visible \u{XXXX}. A file name with U+202E cannot pass for another; a subject cannot hide text.
// One helper for every place a commit's subject, message, author or file names are drawn.
// eslint-disable-next-line no-control-regex, no-misleading-character-class -- these characters are what it finds
const HIDDEN_CHARS = /[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f\u00ad\u061c\u180e\u200b-\u200f\u2028-\u202e\u2060-\u2069\ufe00-\ufe0f\ufeff\u{e0000}-\u{e007f}]/gu

/** s with every hidden character shown as an escape. */
export function visibleText(s: string): string {
  return s.replace(HIDDEN_CHARS, c => `\\u{${c.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')}}`)
}
