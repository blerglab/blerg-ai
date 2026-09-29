import { useState } from "react";

// CodeBlock: fenced code with a micro syntax highlighter — no dependency,
// tuned for glanceability in chat (comments/strings/keywords/numbers), not
// editor fidelity. Language comes from the fence tag, guessed otherwise.

interface LangDef {
  keywords: Set<string>;
  lineComment: string;
  types?: Set<string>;
}

const kw = (s: string) => new Set(s.split(" "));

const LANGS: Record<string, LangDef> = {
  go: {
    keywords: kw("func return if else for range switch case default break continue type struct interface map chan go defer select package import var const nil true false err error"),
    types: kw("string int int64 bool byte rune float64 any"),
    lineComment: "//",
  },
  ts: {
    keywords: kw("function return if else for while switch case default break continue const let var class interface type import export from async await new this null undefined true false try catch throw"),
    types: kw("string number boolean any void unknown never"),
    lineComment: "//",
  },
  py: {
    keywords: kw("def return if elif else for while class import from as with try except finally raise lambda pass break continue None True False and or not in is yield async await"),
    lineComment: "#",
  },
  sh: {
    keywords: kw("if then else elif fi for do done while case esac function echo exit return local export set curl grep sed awk cat"),
    lineComment: "#",
  },
  sql: {
    keywords: kw("select from where insert into values update set delete create table alter add column index join left right on group by order limit and or not null primary key references default"),
    lineComment: "--",
  },
};
const ALIAS: Record<string, string> = {
  golang: "go", typescript: "ts", tsx: "ts", javascript: "ts", js: "ts", jsx: "ts",
  python: "py", bash: "sh", shell: "sh", zsh: "sh", postgres: "sql", postgresql: "sql",
};

function resolveLang(tag: string, code: string): string | null {
  const t = tag.toLowerCase();
  if (LANGS[t]) return t;
  if (ALIAS[t]) return ALIAS[t];
  if (t) return null; // named but unknown — render plain
  // guess
  if (/^\s*(func |package |type \w+ struct)/m.test(code)) return "go";
  if (/^\s*(def |import |from \w+ import)/m.test(code)) return "py";
  if (/^\s*(const |let |function |=>)/m.test(code)) return "ts";
  if (/^\s*(SELECT|INSERT|UPDATE|CREATE)\b/im.test(code)) return "sql";
  if (/^\s*(#!\/|curl |echo |\$\{)/m.test(code)) return "sh";
  return null;
}

// tokenize one line into spans: comment tail, strings, numbers, keywords
function highlightLine(line: string, def: LangDef, key: number): React.ReactNode {
  const ci = line.indexOf(def.lineComment);
  // crude but effective: treat a comment marker outside a string as the tail
  let codePart = line, comment: string | null = null;
  if (ci >= 0 && !isInString(line, ci)) {
    codePart = line.slice(0, ci);
    comment = line.slice(ci);
  }
  const out: React.ReactNode[] = [];
  const re = /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`[^`]*`|\b\d+(?:\.\d+)?\b|\b[A-Za-z_]\w*\b)/g;
  let last = 0, m: RegExpExecArray | null, k = 0;
  while ((m = re.exec(codePart)) !== null) {
    if (m.index > last) out.push(codePart.slice(last, m.index));
    const tok = m[0];
    if (/^["'`]/.test(tok)) out.push(<span className="tk-str" key={k++}>{tok}</span>);
    else if (/^\d/.test(tok)) out.push(<span className="tk-num" key={k++}>{tok}</span>);
    else if (def.keywords.has(tok)) out.push(<span className="tk-kw" key={k++}>{tok}</span>);
    else if (def.types?.has(tok)) out.push(<span className="tk-type" key={k++}>{tok}</span>);
    else out.push(tok);
    last = m.index + tok.length;
  }
  if (last < codePart.length) out.push(codePart.slice(last));
  if (comment != null) out.push(<span className="tk-com" key={k++}>{comment}</span>);
  return <span key={key}>{out}{"\n"}</span>;
}

function isInString(line: string, idx: number): boolean {
  let q: string | null = null;
  for (let i = 0; i < idx; i++) {
    const ch = line[i];
    if (q) {
      if (ch === "\\") i++;
      else if (ch === q) q = null;
    } else if (ch === '"' || ch === "'" || ch === "`") q = ch;
  }
  return q != null;
}

export function CodeBlock({ code, lang }: { code: string; lang: string }) {
  const [copied, setCopied] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const resolved = resolveLang(lang, code);
  const def = resolved ? LANGS[resolved] : null;
  const lines = code.split("\n");

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch { /* clipboard unavailable */ }
  };

  const body = <>{def ? lines.map((l, i) => highlightLine(l, def, i)) : code}</>;
  const head = (
    <div className="codeframe-head">
      <span className="codelang">{lang || resolved || "code"}</span>
      <span className="row" style={{ gap: 10 }}>
        <button type="button" className="codecopy" onClick={copy}>
          {copied ? "copied ✓" : "copy"}
        </button>
        <button type="button" className="codecopy" title={expanded ? "close" : "expand"}
          onClick={(e) => { e.stopPropagation(); setExpanded(!expanded); }}>
          {expanded ? "✕" : "⛶"}
        </button>
      </span>
    </div>
  );

  return (
    <>
      <div className="codeframe">
        {head}
        <pre className="codeblock">{body}</pre>
      </div>
      {expanded && (
        <div className="code-modal-backdrop" onClick={() => setExpanded(false)}>
          <div className="codeframe code-modal" onClick={(e) => e.stopPropagation()}
            role="dialog" aria-label="code">
            {head}
            <pre className="codeblock code-modal-body">{body}</pre>
          </div>
        </div>
      )}
    </>
  );
}
