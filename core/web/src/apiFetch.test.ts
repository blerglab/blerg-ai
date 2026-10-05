import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// vi.mock factories are hoisted above top-level const declarations, so the
// spy itself must be created inside vi.hoisted to be visible in the factory.
const { redirectToRefresh, ensureFreshToken } = vi.hoisted(() => ({
  redirectToRefresh: vi.fn(),
  ensureFreshToken: vi.fn(),
}));

// Real getAccessToken/consumeAccessTokenFromFragment (so the bearer-header
// assertion exercises the actual in-memory token), but a spy in place of
// redirectToRefresh — a real one would try to navigate jsdom's location.
vi.mock("./authClient", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./authClient")>();
  return { ...actual, redirectToRefresh, ensureFreshToken };
});

import { consumeAccessTokenFromFragment } from "./authClient";
import { apiFetch } from "./apiFetch";
import { clearRefreshAttempted } from "./refreshGuard";

const flush = () => new Promise<void>((r) => setTimeout(r, 0));

describe("apiFetch", () => {
  beforeEach(() => {
    redirectToRefresh.mockClear();
    ensureFreshToken.mockReset();
    ensureFreshToken.mockResolvedValue(false);
    window.history.replaceState(null, "", "/");
    window.history.replaceState(null, "", "/#access_token=tok-abc");
    consumeAccessTokenFromFragment();
    clearRefreshAttempted();
    try {
      sessionStorage.clear();
    } catch {
      /* ignore */
    }
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("attaches the bearer token to every request", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true });
    vi.stubGlobal("fetch", fetchMock);

    await apiFetch("/api/me");

    expect(fetchMock).toHaveBeenCalledWith("/api/me", {
      headers: { Authorization: "Bearer tok-abc" },
    });
  });

  it("redirects to core refresh on a 401 instead of resolving with it", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 401, ok: false });
    vi.stubGlobal("fetch", fetchMock);

    let settled = false;
    apiFetch("/api/me").then(() => {
      settled = true;
    });

    // let the fetch promise's .then chain run
    await flush();

    expect(redirectToRefresh).toHaveBeenCalledWith(location.href);
    // the 401 response is never handed back to the caller
    expect(settled).toBe(false);
  });

  it("redirects only once across two separate 401s in the same page load", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 401, ok: false });
    vi.stubGlobal("fetch", fetchMock);

    // First 401: guard not yet tripped -> redirects, response never resolves.
    let firstSettled = false;
    apiFetch("/api/me").then(() => {
      firstSettled = true;
    });
    await flush();
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
    expect(firstSettled).toBe(false);

    // Second 401 (e.g. a second in-flight call landing after the guard was
    // already tripped): must NOT navigate again — instead resolves with the
    // 401 so the caller/UI can show an error rather than loop forever.
    const resp = await apiFetch("/api/me");
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
    expect(resp.status).toBe(401);
  });

  it("renews the token quietly on a 401 and repeats the request once with the new one, with no redirect", async () => {
    ensureFreshToken.mockImplementationOnce(async () => {
      window.history.replaceState(null, "", "/#access_token=tok-new");
      consumeAccessTokenFromFragment();
      return true;
    });
    const fetchMock = vi.fn().mockResolvedValueOnce({ status: 401, ok: false }).mockResolvedValueOnce({ status: 200, ok: true });
    vi.stubGlobal("fetch", fetchMock);

    const resp = await apiFetch("/api/me");

    expect(resp.status).toBe(200);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][1]).toEqual({ headers: { Authorization: "Bearer tok-new" } });
    expect(redirectToRefresh).not.toHaveBeenCalled();
  });

  it("falls back to the page redirect when the repeated request is refused too", async () => {
    ensureFreshToken.mockResolvedValue(true);
    const fetchMock = vi.fn().mockResolvedValue({ status: 401, ok: false });
    vi.stubGlobal("fetch", fetchMock);

    apiFetch("/api/me");
    await flush();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
  });

  it("passthrough401 never tries a quiet renewal", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ status: 401, ok: false }));
    await apiFetch("/auth/password", { method: "POST" }, { passthrough401: true });
    expect(ensureFreshToken).not.toHaveBeenCalled();
  });

  it("passthrough401 hands the 401 back without touching the guard", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 401, ok: false });
    vi.stubGlobal("fetch", fetchMock);

    const resp = await apiFetch("/auth/password", { method: "POST" }, { passthrough401: true });
    expect(resp.status).toBe(401);
    expect(redirectToRefresh).not.toHaveBeenCalled();

    // The guard was not marked by the passthrough call: a later ordinary 401
    // still gets its one redirect.
    let settled = false;
    apiFetch("/api/me").then(() => {
      settled = true;
    });
    await flush();
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
    expect(settled).toBe(false);
  });

  it("does not redirect on a non-401 response", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true });
    vi.stubGlobal("fetch", fetchMock);

    const resp = await apiFetch("/api/me");

    expect(redirectToRefresh).not.toHaveBeenCalled();
    expect(resp.status).toBe(200);
  });
});
