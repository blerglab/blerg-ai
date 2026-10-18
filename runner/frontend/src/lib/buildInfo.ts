// The version line the app shows: the release (the repository's VERSION file) and, when the build
// knows a git commit that differs from it, the commit as a muted suffix: "v0.2.0 · 510d0c0".
// `version` is the older single string (an image tag on a cluster, "dev" on the desktop); it is
// only shown when the server does not report a release.
// `latestVersion` and `latestUrl` name a newer public release (version without the "v", and its
// github.com page) when the server knows of one; formatBuild turns them into `update`.
export type BuildInfo = { version?: string; release?: string; commit?: string; latestVersion?: string; latestUrl?: string };

// Keep in step with the copies in core/web/src/buildInfo.ts and board/web/src/buildInfo.ts (the apps share no package).
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
  // Only an https://github.com/ link to a plain X.Y.Z is ever shown.
  const latest = b?.latestVersion?.trim().replace(/^v/, "") ?? "";
  const href = b?.latestUrl?.trim() ?? "";
  const update = /^\d+\.\d+\.\d+$/.test(latest) && href.startsWith("https://github.com/")
    ? { label: `v${latest} available`, href, title: "A newer Blerg release is available" }
    : null;
  return { main, commit: suffix, title: parts.join(" · "), update };
}
