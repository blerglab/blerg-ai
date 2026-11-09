import ThemeToggle from './ThemeToggle';
import { useEffect, useState, type ReactNode } from "react";
import { formatBuild } from "./buildInfo";

// Shell is the frame every core screen sits in: the wordmark up top (a link back to the
// home), an optional right-hand slot for the signed-in account, and a footer with the build
// version, the /agents manifest, and the theme toggle. Styling comes entirely from the shared
// brand tokens (/brand/tokens.css) — the same palette and type board and runner use — so core
// stops looking like a different product from the two things it signs people into.
//
// Plain anchors, not router Links: Settings and ChangePassword are rendered in tests without a
// router, and a full-page navigation between these few screens costs nothing.

export function Wordmark({ as = "a" }: { as?: "a" | "span" }) {
  const inner = (
    <>
      blerg
      <span className="wordmark-cursor" aria-hidden="true" />
    </>
  );
  return as === "a" ? (
    <a className="wordmark" href="/app" aria-label="blerg home">
      {inner}
    </a>
  ) : (
    <span className="wordmark" aria-label="blerg">
      {inner}
    </span>
  );
}

// Site mirrors GET /api/site (core/internal/api/router.go): the build version and the
// browser-facing component URLs. Public — no token needed — so the footer and the home tiles
// can render before or without identity.
export type Site = { version: string; release?: string; commit?: string; board_url: string; runner_url: string; latest_version?: string; latest_url?: string };

const EMPTY_SITE: Site = { version: "", board_url: "", runner_url: "" };

export function useSite(enabled = true): Site | null {
  const [site, setSite] = useState<Site | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    Promise.resolve()
      .then(() => fetch("/api/site"))
      .then((r) => (r.ok ? r.json() : null))
      .then((s) => {
        if (cancelled) return;
        // A failed read leaves an empty site, not null: every reader falls back on empty fields,
        // and the footer shows no version instead of an ellipsis forever.
        setSite(s && typeof s === "object" ? (s as Site) : EMPTY_SITE);
      })
      .catch(() => {
        if (!cancelled) setSite(EMPTY_SITE);
      });
    return () => {
      cancelled = true;
    };
  }, [enabled]);
  return site;
}

export default function Shell({
  aside,
  children,
  site,
}: {
  aside?: ReactNode;
  children: ReactNode;
  site?: Site | null;
}) {
  const build = formatBuild(site && { ...site, latestVersion: site.latest_version, latestUrl: site.latest_url });
  return (
    <div className="shell">
      <header className="shell-header">
        <Wordmark />
        {aside && <div className="shell-aside">{aside}</div>}
      </header>
      <main className="shell-main">{children}</main>
      <footer className="shell-footer">
        {build.main && (
          <>
            <span className="mono build-version" title={build.title || undefined}>
              Blerg {build.main}
              {build.commit && <span className="build-commit"> · {build.commit}</span>}
            </span>
            {build.update && (
              <>
                <a className="build-update" href={build.update.href} target="_blank" rel="noopener noreferrer" title={build.update.title}>
                  {build.update.label} ↗
                </a>
              </>
            )}
            <span className="sep" aria-hidden="true">
              ·
            </span>
          </>
        )}
        <a href="/agents">/agents manifest</a>
        <ThemeToggle />
      </footer>
    </div>
  );
}
