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
import { api, ApiError, boardSocket } from "./api";
import { clearRefreshAttempted } from "./refreshGuard";

describe("api", () => {
  beforeEach(() => {
    redirectToRefresh.mockClear();
    ensureFreshToken.mockReset();
    ensureFreshToken.mockResolvedValue(false);
    window.history.replaceState(null, "", "/");
    window.history.replaceState(null, "", "/#access_token=tok-xyz");
    consumeAccessTokenFromFragment();
    clearRefreshAttempted();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("attaches the bearer token to every request", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      ok: true,
      text: () => Promise.resolve("{}"),
    });
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/boards");

    const [, opts] = fetchMock.mock.calls[0];
    expect(opts.headers.Authorization).toBe("Bearer tok-xyz");
  });

  it("redirects to core's /auth/refresh on a 401 instead of returning it", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      status: 401,
      ok: false,
      text: () => Promise.resolve("{}"),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api("/api/boards")).rejects.toBeInstanceOf(ApiError);

    expect(redirectToRefresh).toHaveBeenCalledWith(location.href);
  });

  it("renews the token quietly on a 401 and repeats the request once with the new one, with no redirect", async () => {
    ensureFreshToken.mockImplementationOnce(async () => {
      window.history.replaceState(null, "", "/#access_token=tok-new");
      consumeAccessTokenFromFragment();
      return true;
    });
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce({ status: 401, ok: false, text: () => Promise.resolve("{}") })
      .mockResolvedValueOnce({ status: 200, ok: true, text: () => Promise.resolve('{"ok":true}') });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api("/api/boards")).resolves.toEqual({ ok: true });

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(fetchMock.mock.calls[1][1].headers.Authorization).toBe("Bearer tok-new");
    expect(redirectToRefresh).not.toHaveBeenCalled();
  });

  it("falls back to the page redirect when the repeated request is refused too", async () => {
    ensureFreshToken.mockResolvedValue(true);
    const fetchMock = vi.fn().mockResolvedValue({ status: 401, ok: false, text: () => Promise.resolve("{}") });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api("/api/boards")).rejects.toBeInstanceOf(ApiError);

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
  });

  it("does not redirect on a non-401 response", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      ok: true,
      text: () => Promise.resolve("{}"),
    });
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/boards");

    expect(redirectToRefresh).not.toHaveBeenCalled();
  });

  it("navigates to refresh exactly once across two consecutive 401s", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      status: 401,
      ok: false,
      text: () => Promise.resolve("{}"),
    });
    vi.stubGlobal("fetch", fetchMock);

    await expect(api("/api/boards")).rejects.toBeInstanceOf(ApiError);
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);

    // A second 401 (e.g. the refresh didn't fix things, or a racing request)
    // must still throw — callers still see every failed call — but must NOT
    // navigate again, or a broken refresh loops the tab forever.
    await expect(api("/api/boards")).rejects.toBeInstanceOf(ApiError);
    expect(redirectToRefresh).toHaveBeenCalledTimes(1);
  });
});

describe("boardSocket", () => {
  const OriginalWebSocket = globalThis.WebSocket;
  let lastArgs: [string, string | string[] | undefined] | null = null;

  class FakeWebSocket {
    static instances: FakeWebSocket[] = [];
    onopen: (() => void) | null = null;
    onmessage: ((e: MessageEvent) => void) | null = null;
    onclose: (() => void) | null = null;
    closed = false;
    constructor(url: string, protocols?: string | string[]) {
      lastArgs = [url, protocols];
      FakeWebSocket.instances.push(this);
    }
    close() {
      this.closed = true;
    }
  }

  beforeEach(() => {
    lastArgs = null;
    FakeWebSocket.instances = [];
    // @ts-expect-error test stub
    globalThis.WebSocket = FakeWebSocket;
    window.history.replaceState(null, "", "/");
    window.history.replaceState(null, "", "/#access_token=tok-ws");
    consumeAccessTokenFromFragment();
    clearRefreshAttempted();
  });

  afterEach(() => {
    globalThis.WebSocket = OriginalWebSocket;
  });

  it("dials with the bearer subprotocol carrying the access token", () => {
    const cleanup = boardSocket("board-1", () => {});
    expect(lastArgs).not.toBeNull();
    const [, protocols] = lastArgs!;
    expect(protocols).toEqual(["bearer", "tok-ws"]);
    cleanup();
  });

  it("returns a no-op cleanup and opens no socket without an access token", async () => {
    vi.resetModules();
    vi.doMock("./authClient", () => ({
      getAccessToken: () => null,
      redirectToRefresh: vi.fn(),
      consumeAccessTokenFromFragment: () => {},
      coreOrigin: () => "http://core.test",
    }));
    const { boardSocket: boardSocketNoToken } = await import("./api");
    const cleanup = boardSocketNoToken("board-1", () => {});
    expect(FakeWebSocket.instances.length).toBe(0);
    expect(() => cleanup()).not.toThrow();
    vi.doUnmock("./authClient");
    vi.resetModules();
  });
});
