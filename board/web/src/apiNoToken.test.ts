import { describe, it, expect, vi, afterEach } from "vitest";

// A separate file from api.test.ts on purpose: authClient's access token is
// module-level state with no clear(), so "there is no token yet" can't be
// reconstructed once a sibling test has set one.
const { redirectToRefresh } = vi.hoisted(() => ({ redirectToRefresh: vi.fn() }));

vi.mock("./authClient", () => ({
  getAccessToken: () => null,
  redirectToRefresh,
  ensureFreshToken: vi.fn().mockResolvedValue(false),
  consumeAccessTokenFromFragment: () => {},
  coreOrigin: () => "http://core.test",
}));

import { api } from "./api";

describe("api with no access token", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('omits the Authorization header entirely rather than sending "Bearer null"', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      ok: true,
      text: () => Promise.resolve("{}"),
    });
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/boards");

    const [, opts] = fetchMock.mock.calls[0];
    const headers = (opts?.headers ?? {}) as Record<string, string>;
    expect(headers.Authorization).toBeUndefined();
    expect(JSON.stringify(headers)).not.toContain("Bearer null");
  });

  it("preserves caller-supplied headers when there is no token to add", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      status: 200,
      ok: true,
      text: () => Promise.resolve("{}"),
    });
    vi.stubGlobal("fetch", fetchMock);

    await api("/api/boards", { json: { name: "x" } });

    const [, opts] = fetchMock.mock.calls[0];
    const headers = (opts?.headers ?? {}) as Record<string, string>;
    expect(headers["Content-Type"]).toBe("application/json");
  });
});
