import { describe, it, expect } from "vitest";
import { formatBuild } from "./buildInfo";

describe("formatBuild", () => {
  it("shows the release with the commit as a suffix", () => {
    expect(formatBuild({ version: "510d0c0", release: "0.2.0", commit: "510d0c0" })).toMatchObject({ main: "v0.2.0", commit: "510d0c0" });
  });
  it("drops a commit that is the release", () => {
    expect(formatBuild({ release: "0.2.0", commit: "0.2.0" }).commit).toBe("");
  });
  it("falls back to the single version string when no release is reported", () => {
    expect(formatBuild({ version: "dev" })).toMatchObject({ main: "dev", commit: "" });
  });
  it("shows nothing once the read has failed (an empty object)", () => {
    expect(formatBuild({}).main).toBe("");
  });
  it("shows an ellipsis while the site info is loading", () => {
    expect(formatBuild(null).main).toBe("…");
  });
});

describe("formatBuild update notice", () => {
  const url = "https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1";
  it("offers a link to a newer release", () => {
    expect(formatBuild({ release: "0.4.0", latestVersion: "0.4.1", latestUrl: url }).update).toEqual({
      label: "v0.4.1 available",
      href: url,
      title: "A newer Blerg release is available",
    });
  });
  it("offers nothing when absent, malformed or not a github.com link", () => {
    expect(formatBuild({ release: "0.4.0" }).update).toBeNull();
    expect(formatBuild({ release: "0.4.0", latestVersion: "0.4.1" }).update).toBeNull();
    expect(formatBuild({ release: "0.4.0", latestVersion: "soon", latestUrl: url }).update).toBeNull();
    expect(formatBuild({ release: "0.4.0", latestVersion: "0.4.1", latestUrl: "https://evil.example/x" }).update).toBeNull();
    expect(formatBuild({ release: "0.4.0", latestVersion: "0.4.1", latestUrl: "javascript:alert(1)" }).update).toBeNull();
    expect(formatBuild(null).update).toBeNull();
  });
});
