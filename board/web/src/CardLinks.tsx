import { ReactNode } from "react";
import { CardLink } from "./api";

// safeHref returns the URL only when it is an http(s) address: a link is user- or agent-supplied text, and an
// anchor with a javascript: or data: address would run it. Anything else is shown as plain text.
export function safeHref(url: string): string | null {
  try {
    const u = new URL(url);
    return u.protocol === "https:" || u.protocol === "http:" ? u.toString() : null;
  } catch {
    return null;
  }
}

// SafeAnchor is an <a> for an address that is safe to follow (http or https, opened in a new tab without
// the opener), and plain text for anything else.
export function SafeAnchor({ href, className, title, children }: {
  href: string | null | undefined; className?: string; title?: string; children: ReactNode;
}) {
  const safe = href ? safeHref(href) : null;
  if (!safe) return <span className={className} title="Not a web address, so it is not made a link">{children}</span>;
  return <a className={className} href={safe} title={title} target="_blank" rel="noreferrer noopener">{children}</a>;
}

// dedupedLinks collapses repeat links (same kind + label — e.g. one "runner session" per spawn attempt) down
// to the most recent occurrence. A file published by a runner session is keyed by its address instead: two
// sessions can each publish a "report.pdf (v1)" and both belong on the card.
export function dedupedLinks(links: CardLink[] | null | undefined): CardLink[] {
  const seen = new Set<string>();
  const out: CardLink[] = [];
  for (const l of (links ?? []).slice().reverse()) {
    const k = l.kind === "artifact" ? `artifact|${l.url}` : `${l.kind}|${l.label || l.url}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.unshift(l);
  }
  return out;
}

export function CardLinks({ links, onOpenDoc }: { links: CardLink[] | null | undefined; onOpenDoc: (l: CardLink) => void }) {
  const shown = dedupedLinks(links);
  if (shown.length === 0) return null;
  return (
    <section>
      <h4>Links</h4>
      {shown.map((l) => {
        const href = l.kind === "doc" ? null : safeHref(l.url);
        const text = l.label || l.url;
        return (
          <div key={l.kind + l.url}>
            {l.kind === "doc" ? (
              <button type="button" className="chip doc" onClick={() => onOpenDoc(l)}
                title="open in blerg-board">doc</button>
            ) : (
              <span className={l.kind === "artifact" ? "chip artifact" : "chip"}>{l.kind}</span>
            )}{" "}
            {l.kind === "doc" ? (
              <button type="button" className="link-btn" onClick={() => onOpenDoc(l)}>{text}</button>
            ) : href ? (
              <a href={href} target="_blank" rel="noreferrer noopener"
                title={l.kind === "artifact" ? "Open this file in the runner" : undefined}>
                {text}{l.kind === "artifact" && <span aria-hidden="true"> ↗</span>}
              </a>
            ) : (
              <span title="Not a web address, so it is not made a link">{text}</span>
            )}
          </div>
        );
      })}
    </section>
  );
}
