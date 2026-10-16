import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// The board's half of the stuck-tab fix (runner's twin: runner/frontend/src/ws.test.ts). Each
// test re-evaluates the modules, which is what a page load does: refreshGuard.ts's beginPageLoad
// runs again against whatever sessionStorage and the address hold.
const { redirectToRefresh } = vi.hoisted(() => ({ redirectToRefresh: vi.fn() }));

vi.mock("./authClient", () => ({
  getAccessToken: () => null,
  ensureFreshToken: vi.fn().mockResolvedValue(false),
  redirectToRefresh,
}));

const unauthorized = () =>
  vi.fn().mockResolvedValue({ status: 401, ok: false, text: () => Promise.resolve("{}") });

async function load() {
  vi.resetModules();
  const { api } = await import("./api");
  const { signInStuck } = await import("./signIn");
  const { refreshReturnUrl, MAX_ATTEMPTS } = await import("./refreshGuard");
  return { api, signInStuck, refreshReturnUrl, MAX_ATTEMPTS };
}

describe("board page load and the refresh guard", () => {
  beforeEach(() => {
    redirectToRefresh.mockClear();
    sessionStorage.clear();
    window.history.replaceState(null, "", "/");
    vi.stubGlobal("fetch", unauthorized());
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("a stale attempted flag from an earlier load is ignored: the first 401 bounces", async () => {
    sessionStorage.setItem("blerg_refresh_attempted", "1");
    const { api, refreshReturnUrl } = await load();

    await expect(api("/api/boards")).rejects.toThrow();

    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
    expect(redirectToRefresh).toHaveBeenCalledWith(refreshReturnUrl());
  });

  it("the return leg from /auth/refresh without a token does not bounce again; it raises the banner", async () => {
    sessionStorage.setItem("blerg_refresh_attempted", "1");
    window.history.replaceState(null, "", "/boards#blerg_refresh_return");
    const { api, signInStuck } = await load();

    await expect(api("/api/boards")).rejects.toThrow();

    expect(redirectToRefresh).not.toHaveBeenCalled();
    expect(signInStuck()).toBe(true);
    expect(location.hash).toBe(""); // a reload from here is a fresh load
  });

  it("the rolling counter still caps fresh-load bounces at MAX_ATTEMPTS", async () => {
    let page = await load();
    for (let i = 0; i < page.MAX_ATTEMPTS; i++) {
      page = await load();
      await expect(page.api("/api/boards")).rejects.toThrow();
    }
    expect(redirectToRefresh).toHaveBeenCalledTimes(page.MAX_ATTEMPTS);

    page = await load();
    await expect(page.api("/api/boards")).rejects.toThrow();
    expect(redirectToRefresh).toHaveBeenCalledTimes(page.MAX_ATTEMPTS);
    expect(page.signInStuck()).toBe(true);
  });
});
