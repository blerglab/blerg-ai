import { useEffect, useState, type ReactNode } from "react";

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

// Theme toggle: dark is the default; a pinned choice persists (localStorage "blerg-theme",
// shared with board/runner and index.html's pre-paint script) and beats the OS preference.
function effectiveTheme(): "light" | "dark" {
  const pinned = document.documentElement.getAttribute("data-theme");
  if (pinned === "light" || pinned === "dark") return pinned;
  return window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark";
}

function ThemeToggle() {
  const [theme, setTheme] = useState<"light" | "dark">(() => effectiveTheme());
  const toggle = () => {
    const next = theme === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    try {
      localStorage.setItem("blerg-theme", next);
    } catch {
      // private mode / blocked storage: the choice still applies for this page
    }
    setTheme(next);
  };
  return (
    <button
      type="button"
      className="theme-toggle"
      onClick={toggle}
      title={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
      aria-label={theme === "dark" ? "Switch to light theme" : "Switch to dark theme"}
    >
      {theme === "dark" ? (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <circle cx="12" cy="12" r="4" />
          <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
        </svg>
      ) : (
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
        </svg>
      )}
    </button>
  );
}

// Site mirrors GET /api/site (core/internal/api/router.go): the build version and the
// browser-facing component URLs. Public — no token needed — so the footer and the home tiles
// can render before or without identity.
export type Site = { version: string; board_url: string; runner_url: string };

export function useSite(enabled = true): Site | null {
  const [site, setSite] = useState<Site | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    Promise.resolve()
      .then(() => fetch("/api/site"))
      .then((r) => (r.ok ? r.json() : null))
      .then((s) => {
        if (!cancelled && s && typeof s === "object") setSite(s as Site);
      })
      .catch(() => {});
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
  return (
    <div className="shell">
      <header className="shell-header">
        <Wordmark />
        {aside && <div className="shell-aside">{aside}</div>}
      </header>
      <main className="shell-main">{children}</main>
      <footer className="shell-footer">
        <span className="mono">blerg-core {site?.version ?? "…"}</span>
        <span className="sep" aria-hidden="true">
          ·
        </span>
        <a href="/agents">/agents manifest</a>
        <ThemeToggle />
      </footer>
    </div>
  );
}
