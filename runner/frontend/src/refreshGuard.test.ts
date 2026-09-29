import { describe, it, expect, beforeEach } from "vitest";
import { refreshAlreadyAttempted, markRefreshAttempted, clearRefreshAttempted, MAX_ATTEMPTS, WINDOW_MS } from "./refreshGuard";

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
