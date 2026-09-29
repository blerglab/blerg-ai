import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import App from "./App";
import * as authClient from "./authClient";

// A fetch stub that answers by URL: /api/me with the given identity, /api/site with component
// URLs, and anything else (the tiles' /healthz probes) with a bare 200.
function fetchByUrl(me: Record<string, unknown> | null) {
  return vi.fn((url: string) => {
    if (url === "/api/me") {
      return me
        ? Promise.resolve({ ok: true, json: () => Promise.resolve(me) })
        : Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) });
    }
    if (url === "/api/site") {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ version: "v-test", board_url: "https://board.example.com", runner_url: "https://runner.example.com" }),
      });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
  });
}

const alice = {
  account_id: "acc-1",
  provider: "local",
  provider_subject: "alice",
  email: "alice@example.com",
  role: "admin",
  must_change_password: false,
};

describe("App / Home", () => {
  // App renders Home at "/app" via BrowserRouter, which reads the real browser location —
  // point it at /app before each render. refreshGuard keeps its once-per-load flag in
  // sessionStorage, which jsdom shares across tests here.
  beforeEach(() => {
    window.history.pushState({}, "", "/app");
    sessionStorage.clear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("shows a generic signed-in message before GET /api/me resolves, then the account identity", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    vi.stubGlobal("fetch", fetchByUrl(alice));

    render(<App />);

    expect(screen.getByText("You're signed in.")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText(/Signed in as alice \(admin\)/)).toBeInTheDocument());
  });

  it("never decodes the access token client-side for identity — falls back to the generic message on a failed /api/me", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    vi.stubGlobal("fetch", fetchByUrl(null));

    render(<App />);

    await waitFor(() => expect(screen.getByText("You're signed in.")).toBeInTheDocument());
    expect(screen.queryByText(/Signed in as/)).not.toBeInTheDocument();
  });

  // The front door: a signed-out visitor is sent through /auth/refresh (which lands on the
  // login form when there is no session) rather than shown a page with a "Sign in" link to
  // notice. No identity call is made without a token.
  it("redirects a signed-out visitor through /auth/refresh and never calls /api/me", () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue(null);
    const redirect = vi.spyOn(authClient, "redirectToRefresh").mockImplementation(() => {});
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);

    expect(redirect).toHaveBeenCalledWith(`${location.origin}/app`);
    expect(fetchMock).not.toHaveBeenCalledWith("/api/me");
    expect(screen.queryByText("Sign in")).not.toBeInTheDocument();
  });

  it("offers a manual link to sign in when the refresh guard says it already bounced once", () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue(null);
    const redirect = vi.spyOn(authClient, "redirectToRefresh").mockImplementation(() => {});
    vi.stubGlobal("fetch", vi.fn());
    sessionStorage.setItem("blerg_refresh_attempted", "1");

    render(<App />);

    expect(redirect).not.toHaveBeenCalled();
    expect(screen.getByText("Go to sign in")).toHaveAttribute("href", "/login");
  });

  it("renders the component tiles from /api/site with a live status chip", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    const fetchMock = fetchByUrl(alice);
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);

    const board = await screen.findByRole("link", { name: /Board/ });
    await waitFor(() => expect(board).toHaveAttribute("href", "https://board.example.com"));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("https://board.example.com/healthz", expect.anything()));
    await waitFor(() => expect(screen.getAllByText("Up").length).toBeGreaterThan(0));
  });

  it("links to settings and the change-password page when signed in", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    vi.stubGlobal("fetch", fetchByUrl({ ...alice, role: "member" }));
    render(<App />);
    expect(await screen.findByText("Change password")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Change password/ })).toHaveAttribute("href", "/change-password");
    expect(screen.getByRole("link", { name: /Settings/ })).toHaveAttribute("href", "/settings");
  });
});
