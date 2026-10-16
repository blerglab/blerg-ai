import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import ChangePassword, { postChangeLoginURL } from "./ChangePassword";
import * as authClient from "./authClient";

// These tests exercise the REAL apiFetch (only global fetch is mocked), so the wrong-password
// case actually proves apiFetch's passthrough401 opt-out works end to end: without it, a 401
// here would be intercepted by apiFetch and turned into a redirectToRefresh navigation instead
// of ever reaching this component — "Current password is incorrect." would never render.
describe("ChangePassword", () => {
  beforeEach(() => {
    // refreshGuard.ts keeps its once-per-load flag in sessionStorage, which jsdom shares across
    // tests in this file — a refresh triggered by one test must not suppress the next one's.
    sessionStorage.clear();
    vi.spyOn(authClient, "getAccessToken").mockReturnValue("test-access-token");
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  function fillForm(old: string, next: string, confirm: string) {
    fireEvent.change(screen.getByLabelText("Current password"), { target: { value: old } });
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: next } });
    fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: confirm } });
    fireEvent.click(screen.getByRole("button", { name: /Change password/ }));
  }

  it("shows an inline error for a wrong old password (401) WITHOUT redirecting to /auth/refresh", async () => {
    const redirect = vi.spyOn(authClient, "redirectToRefresh").mockImplementation(() => {});
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve({
          ok: false,
          status: 401,
          headers: new Headers({ "X-Blerg-Error": "invalid_credentials" }),
          json: () => Promise.resolve({}),
        }),
      ),
    );

    render(<ChangePassword />);
    fillForm("wrong-old-password", "a-long-enough-new-password", "a-long-enough-new-password");

    await waitFor(() =>
      expect(screen.getByText("Current password is incorrect.")).toBeInTheDocument(),
    );
    // The whole point of passthrough401: apiFetch's default 401 handling (redirect the page to
    // /auth/refresh) must NOT have fired for this call.
    expect(redirect).not.toHaveBeenCalled();
    // And the button must not be stuck disabled forever.
    expect(screen.getByRole("button", { name: "Change password" })).not.toBeDisabled();
  });

  // The 401 the first admin actually hit: the 10-minute access token expired while the form sat
  // open. The middleware's 401 carries no X-Blerg-Error, and the right move is a refresh back to
  // this page — NOT "Current password is incorrect."
  it("refreshes on a stale-token 401 (no X-Blerg-Error) instead of blaming the password", async () => {
    const redirect = vi.spyOn(authClient, "redirectToRefresh").mockImplementation(() => {});
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve({ ok: false, status: 401, headers: new Headers(), json: () => Promise.resolve({}) }),
      ),
    );

    render(<ChangePassword />);
    fillForm("old-password-123", "a-long-enough-new-password", "a-long-enough-new-password");

    await waitFor(() => expect(redirect).toHaveBeenCalledWith(location.href));
    expect(screen.queryByText("Current password is incorrect.")).not.toBeInTheDocument();
  });

  it("refreshes immediately when no access token is in memory (a reload)", () => {
    vi.spyOn(authClient, "getAccessToken").mockReturnValue(null);
    const redirect = vi.spyOn(authClient, "redirectToRefresh").mockImplementation(() => {});
    vi.stubGlobal("fetch", vi.fn());

    render(<ChangePassword />);

    expect(redirect).toHaveBeenCalledWith(location.href);
  });

  it("shows an inline error for a weak new password (400)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve({ ok: false, status: 400, json: () => Promise.resolve({}) })),
    );

    render(<ChangePassword />);
    fillForm("old-password-123", "aaaaaaaaaaaaaa", "aaaaaaaaaaaaaa");

    await waitFor(() =>
      expect(screen.getByText("Password must be at least 12 characters.")).toBeInTheDocument(),
    );
  });

  it("shows a mismatch error without ever calling the server when confirm doesn't match", () => {
    // The page reads GET /api/me on mount to prefill the username; the submit must not POST.
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
      Promise.resolve({ ok: false, status: 404, headers: new Headers(), json: () => Promise.resolve({}) }),
    );
    vi.stubGlobal("fetch", fetchMock);

    render(<ChangePassword />);
    fillForm("old-password-123", "a-long-enough-new-password", "does-not-match");

    expect(screen.getByText("New password and confirmation don't match.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url]) => url === "/auth/password")).toBe(false);
  });

  // The user who was diverted here on their way to board must end up back at board after the
  // forced re-login, not on core's /app.
  it("carries return_to from this page's query string through to /login", () => {
    expect(postChangeLoginURL("")).toBe("/login?changed=1");
    expect(postChangeLoginURL("?return_to=https%3A%2F%2Fboard.example.com%2F")).toBe(
      "/login?changed=1&return_to=https%3A%2F%2Fboard.example.com%2F",
    );
  });

  it("tells /login the username changed, so it can say so", () => {
    expect(postChangeLoginURL("", true)).toBe("/login?changed=1&user=1");
    expect(postChangeLoginURL("?return_to=https%3A%2F%2Fboard.example.com%2F", true)).toBe(
      "/login?changed=1&user=1&return_to=https%3A%2F%2Fboard.example.com%2F",
    );
  });

  describe("choosing a username", () => {
    // serve answers GET /api/me with the random bootstrap username and POST /auth/password with
    // `password`, recording the POST body.
    function serve(password: { status: number; headers?: Headers; body?: unknown }) {
      const posted: { body?: Record<string, unknown> } = {};
      const fetchMock = vi.fn((url: string, init?: RequestInit) => {
        if (url === "/api/me") {
          return Promise.resolve({
            ok: true,
            status: 200,
            headers: new Headers(),
            json: () =>
              Promise.resolve({ provider: "local", provider_subject: "Rand0m_Boot", must_change_password: true }),
          });
        }
        posted.body = JSON.parse(init!.body as string) as Record<string, unknown>;
        return Promise.resolve({
          ok: password.status >= 200 && password.status < 300,
          status: password.status,
          headers: password.headers ?? new Headers(),
          json: () => Promise.resolve(password.body ?? {}),
        });
      });
      vi.stubGlobal("fetch", fetchMock);
      return posted;
    }

    it("prefills the current username and explains what it is", async () => {
      serve({ status: 200 });
      render(<ChangePassword />);
      const field = screen.getByLabelText("Choose your username") as HTMLInputElement;
      await waitFor(() => expect(field.value).toBe("Rand0m_Boot"));
      expect(
        screen.getByText("This is the name you sign in with. Lowercase letters, digits, dots, dashes and underscores."),
      ).toBeInTheDocument();
    });

    it("leaves provider_subject out of the request when the username is left alone", async () => {
      const posted = serve({ status: 500 });
      render(<ChangePassword />);
      await waitFor(() => expect((screen.getByLabelText("Choose your username") as HTMLInputElement).value).toBe("Rand0m_Boot"));
      fillForm("old-password-123", "a-long-enough-new-password", "a-long-enough-new-password");
      await waitFor(() => expect(posted.body).toBeDefined());
      expect(posted.body).not.toHaveProperty("provider_subject");
    });

    it("sends the new username when it was changed", async () => {
      const posted = serve({ status: 500 });
      render(<ChangePassword />);
      const field = screen.getByLabelText("Choose your username") as HTMLInputElement;
      await waitFor(() => expect(field.value).toBe("Rand0m_Boot"));
      fireEvent.change(field, { target: { value: "alice" } });
      fillForm("old-password-123", "a-long-enough-new-password", "a-long-enough-new-password");
      await waitFor(() => expect(posted.body).toBeDefined());
      expect(posted.body).toMatchObject({
        old_password: "old-password-123",
        new_password: "a-long-enough-new-password",
        provider_subject: "alice",
      });
    });

    it("shows a taken username (409) by the username field, not as a password error", async () => {
      serve({
        status: 409,
        headers: new Headers({ "X-Blerg-Error": "username" }),
        body: { error: "that username is taken" },
      });
      render(<ChangePassword />);
      const field = screen.getByLabelText("Choose your username") as HTMLInputElement;
      await waitFor(() => expect(field.value).toBe("Rand0m_Boot"));
      fireEvent.change(field, { target: { value: "taken" } });
      fillForm("old-password-123", "a-long-enough-new-password", "a-long-enough-new-password");

      const msg = await screen.findByText("that username is taken");
      expect(field.closest("label")!.parentElement!.contains(msg)).toBe(true);
      expect(field).toHaveAttribute("aria-invalid", "true");
      expect(screen.queryByText("Password must be at least 12 characters.")).not.toBeInTheDocument();
    });

    it("shows core's words for an invalid username (400 marked username)", async () => {
      serve({
        status: 400,
        headers: new Headers({ "X-Blerg-Error": "username" }),
        body: { error: "use lowercase letters, digits, dots, dashes and underscores (3–64 characters)" },
      });
      render(<ChangePassword />);
      const field = screen.getByLabelText("Choose your username") as HTMLInputElement;
      await waitFor(() => expect(field.value).toBe("Rand0m_Boot"));
      fireEvent.change(field, { target: { value: "Alice" } });
      fillForm("old-password-123", "a-long-enough-new-password", "a-long-enough-new-password");

      expect(
        await screen.findByText("use lowercase letters, digits, dots, dashes and underscores (3–64 characters)"),
      ).toBeInTheDocument();
      expect(screen.queryByText("Password must be at least 12 characters.")).not.toBeInTheDocument();
    });
  });
});
