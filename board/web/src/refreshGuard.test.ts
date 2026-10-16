import { describe, it, expect, beforeEach } from "vitest";
import { refreshAlreadyAttempted, markRefreshAttempted, clearRefreshAttempted, beginPageLoad, refreshReturnUrl, resetRefreshGuard, RETURN_MARKER, MAX_ATTEMPTS, WINDOW_MS } from "./refreshGuard";

describe("refreshGuard", () => {
  beforeEach(() => { sessionStorage.clear(); });

  it("allows one attempt per load and blocks the second until cleared", () => {
    expect(refreshAlreadyAttempted()).toBe(false);
    markRefreshAttempted();
    expect(refreshAlreadyAttempted()).toBe(true);
    clearRefreshAttempted();
    expect(refreshAlreadyAttempted()).toBe(false);
  });

  it("caps attempts within the window even when each one is cleared by a success", () => {
    const t0 = 1_000_000;
    for (let i = 0; i < MAX_ATTEMPTS; i++) {
      expect(refreshAlreadyAttempted(t0 + i)).toBe(false);
      markRefreshAttempted(t0 + i);
      clearRefreshAttempted(); // simulate "token consumed" after each redirect
    }
    expect(refreshAlreadyAttempted(t0 + MAX_ATTEMPTS)).toBe(true);
  });

  it("resets the counter after the window elapses", () => {
    const t0 = 1_000_000;
    for (let i = 0; i < MAX_ATTEMPTS; i++) { markRefreshAttempted(t0); clearRefreshAttempted(); }
    expect(refreshAlreadyAttempted(t0 + 1)).toBe(true);
    expect(refreshAlreadyAttempted(t0 + WINDOW_MS + 1)).toBe(false);
  });

  it("tolerates corrupt counter state", () => {
    sessionStorage.setItem("blerg_refresh_attempts", "not json");
    expect(refreshAlreadyAttempted()).toBe(false);
    markRefreshAttempted();
    expect(refreshAlreadyAttempted()).toBe(true);
  });

  it("resets when the stored window start is in the future", () => {
    const now = 1_000_000;
    sessionStorage.setItem("blerg_refresh_attempts", JSON.stringify({ n: 3, since: now + 10 * WINDOW_MS }));
    expect(refreshAlreadyAttempted(now)).toBe(false);
  });
});

describe("refreshGuard page load", () => {
  beforeEach(() => {
    sessionStorage.clear();
    window.history.replaceState(null, "", "/");
  });

  it("a fresh load drops an attempted flag left by an earlier load, but not the counter", () => {
    markRefreshAttempted();
    expect(beginPageLoad()).toBe(false);
    expect(sessionStorage.getItem("blerg_refresh_attempted")).toBeNull();
    expect(sessionStorage.getItem("blerg_refresh_attempts")).not.toBeNull();
  });

  it("the tokenless return leg keeps the flag and strips the marker", () => {
    markRefreshAttempted();
    window.history.replaceState(null, "", "/s/x?y=1#" + RETURN_MARKER);
    expect(beginPageLoad()).toBe(true);
    expect(refreshAlreadyAttempted()).toBe(true);
    expect(location.pathname + location.search + location.hash).toBe("/s/x?y=1");
  });

  it("a return leg that carried a token is a fresh load and leaves the fragment for authClient", () => {
    markRefreshAttempted();
    window.history.replaceState(null, "", "/#" + RETURN_MARKER + "#access_token=tok");
    expect(beginPageLoad()).toBe(false);
    expect(refreshAlreadyAttempted()).toBe(false);
    expect(location.hash).toContain("access_token=tok");
  });

  it("refreshReturnUrl marks this page as the return leg, replacing any fragment", () => {
    expect(refreshReturnUrl("https://runner.example/s/1?a=b#old")).toBe("https://runner.example/s/1?a=b#" + RETURN_MARKER);
  });

  it("resetRefreshGuard clears both keys", () => {
    for (let i = 0; i < MAX_ATTEMPTS; i++) markRefreshAttempted();
    expect(refreshAlreadyAttempted()).toBe(true);
    resetRefreshGuard();
    expect(refreshAlreadyAttempted()).toBe(false);
    expect(sessionStorage.length).toBe(0);
  });
});
