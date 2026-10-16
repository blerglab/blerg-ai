import { useEffect, useState } from "react";
import { api } from "./api";

// The version the footer shows: the release (the repository's VERSION file) and, when the build
// knows a git commit that differs from it, the commit as a muted suffix: "v0.2.0 · 510d0c0".
// `version` is the older single string (an image tag on a cluster, "dev" on the desktop); it is
// only shown when the server does not report a release.
export type BuildInfo = { version?: string; release?: string; commit?: string };

// Keep in step with the copies in core/web/src/buildInfo.ts and runner/frontend/src/lib/buildInfo.ts (the apps share no package).
// null means "still loading" (main is an ellipsis); an object with nothing in it means "unknown"
// (main is empty, and the caller renders nothing).
export function formatBuild(b: BuildInfo | null | undefined): { main: string; commit: string; title: string } {
  const release = b?.release?.trim() ?? "";
  const version = b?.version?.trim() ?? "";
  const commit = b?.commit?.trim() ?? "";
  const main = release ? `v${release}` : version || (b ? "" : "…");
  const suffix = commit && commit !== release && commit !== main ? commit : "";
  const parts = [release && `release ${release}`, commit && `commit ${commit}`, version && `build ${version}`].filter(Boolean);
  return { main, commit: suffix, title: parts.join(" · ") };
}

// useBuildInfo reads GET /api/version (public, static) once per mount. A failed read yields {}:
// the top bar then shows nothing rather than an ellipsis forever.
export function useBuildInfo(): BuildInfo | null {
  const [info, setInfo] = useState<BuildInfo | null>(null);
  useEffect(() => {
    let cancelled = false;
    api<BuildInfo>("/api/version")
      .then((b) => { if (!cancelled && b && typeof b === "object") setInfo(b); })
      .catch(() => { if (!cancelled) setInfo({}); });
    return () => { cancelled = true; };
  }, []);
  return info;
}
