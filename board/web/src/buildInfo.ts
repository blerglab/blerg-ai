import { useEffect, useState } from "react";
import { api } from "./api";

// The version the footer shows: the release (the repository's VERSION file) and, when the build
// knows a git commit that differs from it, the commit as a muted suffix: "v0.2.0 · 510d0c0".
// `version` is the older single string (an image tag on a cluster, "dev" on the desktop); it is
// only shown when the server does not report a release.
// `latestVersion` and `latestUrl` name a newer public release (version without the "v", and its
// github.com page) when the server knows of one; formatBuild turns them into `update`.
export type BuildInfo = { version?: string; release?: string; commit?: string; latestVersion?: string; latestUrl?: string };

// Keep in step with the copies in core/web/src/buildInfo.ts and runner/frontend/src/lib/buildInfo.ts (the apps share no package).
// null means "still loading" (main is an ellipsis); an object with nothing in it means "unknown"
// (main is empty, and the caller renders nothing).
export type BuildUpdate = { label: string; href: string; title: string };
export function formatBuild(b: BuildInfo | null | undefined): { main: string; commit: string; title: string; update: BuildUpdate | null } {
  const release = b?.release?.trim() ?? "";
  const version = b?.version?.trim() ?? "";
  const commit = b?.commit?.trim() ?? "";
  const main = release ? `v${release}` : version || (b ? "" : "…");
  const suffix = commit && commit !== release && commit !== main ? commit : "";
  const parts = [release && `release ${release}`, commit && `commit ${commit}`, version && `build ${version}`].filter(Boolean);
  // Match the exact public release tag and compare against this component's release.
  const latest = b?.latestVersion?.trim().replace(/^v/, "") ?? "";
  const href = b?.latestUrl ?? "";
  const stable = /^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$/;
  const running = release.replace(/^v/, "");
  const candidate = latest.split(".").map(Number);
  const current = running.split(".").map(Number);
  const different = candidate.findIndex((n, i) => n !== current[i]);
  const newer = stable.test(latest) && stable.test(running) && different >= 0 && candidate[different] > current[different];
  const prefix = "https://github.com/blerglab/blerg-ai/releases/tag/";
  const update = newer && (href === prefix + latest || href === prefix + "v" + latest)
    ? { label: `v${latest} available`, href, title: "A newer Blerg release is available" }
    : null;
  return { main, commit: suffix, title: parts.join(" · "), update };
}

// useBuildInfo reads GET /api/version (public, static) once per mount. A failed read yields {}:
// the top bar then shows nothing rather than an ellipsis forever.
type VersionResponse = { version?: string; release?: string; commit?: string; latest_version?: string; latest_url?: string };

export function useBuildInfo(): BuildInfo | null {
  const [info, setInfo] = useState<BuildInfo | null>(null);
  useEffect(() => {
    let cancelled = false;
    api<VersionResponse>("/api/version")
      .then((b) => {
        if (!cancelled && b && typeof b === "object") {
          setInfo({ version: b.version, release: b.release, commit: b.commit, latestVersion: b.latest_version, latestUrl: b.latest_url });
        }
      })
      .catch(() => { if (!cancelled) setInfo({}); });
    return () => { cancelled = true; };
  }, []);
  return info;
}
