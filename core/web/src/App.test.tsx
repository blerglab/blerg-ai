import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import App from "./App";
import * as authClient from "./authClient";

// A fetch stub that answers by URL: /api/me with the given identity, /api/site with component
// URLs, and anything else (the tiles' /healthz probes) with a bare 200.
function fetchByUrl(me: Record<string, unknown> | null, siteExtra: Record<string, unknown> = {}) {
  return vi.fn((url: string) => {
    if (url === "/api/me") {
      return me
        ? Promise.resolve({ ok: true, json: () => Promise.resolve(me) })
        : Promise.resolve({ ok: false, status: 500, json: () => Promise.resolve({}) });
    }
    if (url === "/api/site") {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ version: "v-test", release: "0.2.0", commit: "510d0c0", board_url: "https://board.example.com", runner_url: "https://runner.example.com", ...siteExtra }),
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

  it("clears both refresh-guard keys on Log out, so a login in this tab starts clean", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    vi.stubGlobal("fetch", fetchByUrl(alice));
    sessionStorage.setItem("blerg_refresh_attempted", "1");
    sessionStorage.setItem("blerg_refresh_attempts", JSON.stringify({ n: 3, since: Date.now() }));

    render(<App />);
    fireEvent.click(screen.getByRole("button", { name: "Log out" }));

    await waitFor(() => expect(sessionStorage.getItem("blerg_refresh_attempts")).toBeNull());
    expect(sessionStorage.getItem("blerg_refresh_attempted")).toBeNull();
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

  it("shows the release version and commit in the footer", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    vi.stubGlobal("fetch", fetchByUrl(alice));
    render(<App />);
    const footer = await screen.findByText(/Blerg v0\.2\.0/);
    expect(footer).toHaveTextContent("Blerg v0.2.0 · 510d0c0");
    expect(footer).toHaveAttribute("title", expect.stringContaining("release 0.2.0"));
  });

  it("links to a newer release in the footer only when /api/site reports one", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    vi.stubGlobal("fetch", fetchByUrl(alice));
    const first = render(<App />);
    await screen.findByText(/Blerg v0\.2\.0/);
    expect(document.querySelector(".build-update")).toBeNull();
    first.unmount();
    vi.stubGlobal("fetch", fetchByUrl(alice, { latest_version: "0.4.1", latest_url: "https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1" }));
    render(<App />);
    const link = await screen.findByRole("link", { name: /v0\.4\.1 available/ });
    expect(link).toHaveAttribute("href", "https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link).toHaveAttribute("title", "A newer Blerg release is available");
    expect(screen.queryByRole("link", { name: "How to upgrade" })).toBeNull();
  });

  it("shows no version in the footer when /api/site fails", async () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
    const ok = fetchByUrl(alice);
    vi.stubGlobal("fetch", vi.fn((url: string) => (url === "/api/site" ? Promise.reject(new Error("down")) : ok(url))));
    render(<App />);
    await screen.findByText("/agents manifest");
    await waitFor(() => expect(document.querySelector(".build-version")).toBeNull());
    expect(screen.queryByText("…")).toBeNull();
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
