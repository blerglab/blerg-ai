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
