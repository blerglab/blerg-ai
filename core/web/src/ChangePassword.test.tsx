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
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    render(<ChangePassword />);
    fillForm("old-password-123", "a-long-enough-new-password", "does-not-match");

    expect(screen.getByText("New password and confirmation don't match.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  // The user who was diverted here on their way to board must end up back at board after the
  // forced re-login, not on core's /app.
  it("carries return_to from this page's query string through to /login", () => {
    expect(postChangeLoginURL("")).toBe("/login?changed=1");
    expect(postChangeLoginURL("?return_to=https%3A%2F%2Fboard.example.com%2F")).toBe(
      "/login?changed=1&return_to=https%3A%2F%2Fboard.example.com%2F",
    );
  });
});
