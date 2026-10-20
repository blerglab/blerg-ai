import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import Setup, { setupErrorFor } from "./Setup";
import * as authClient from "./authClient";

type Call = [string, RequestInit | undefined];

// fetchFor answers GET /auth/provider with the given setup mode and POST /auth/setup with `post`.
function fetchFor(setup: string | undefined, post?: { status: number; tag?: string; error?: string }) {
  return vi.fn((url: string) => {
    if (url === "/auth/provider") {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(setup ? { id: "local", setup } : { id: "local" }) });
    }
    const p = post ?? { status: 200 };
    return Promise.resolve({
      ok: p.status === 200,
      status: p.status,
      headers: new Headers(p.tag ? { "X-Blerg-Error": p.tag } : {}),
      json: () => Promise.resolve(p.error ? { error: p.error } : {}),
    });
  });
}

function fill(username: string, password: string, confirm: string) {
  fireEvent.change(screen.getByLabelText("Username"), { target: { value: username } });
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: password } });
  fireEvent.change(screen.getByLabelText("Confirm password"), { target: { value: confirm } });
  fireEvent.click(screen.getByRole("button", { name: "Create account" }));
}

describe("Setup", () => {
  beforeEach(() => window.history.pushState({}, "", "/setup"));
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("asks for the setup link when a code is needed and the URL has none", async () => {
    vi.stubGlobal("fetch", fetchFor("code"));
    render(<Setup />);
    expect(await screen.findByText(/Open the setup link from the installer's output/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Username")).toBeNull();
  });

  it("creates the account with the code from the link and goes to the app", async () => {
    window.history.pushState({}, "", "/setup#code=the-code");
    const redirect = vi.spyOn(authClient, "redirectToRefresh").mockImplementation(() => {});
    const fetchMock = fetchFor("code");
    vi.stubGlobal("fetch", fetchMock);
    render(<Setup />);
    await screen.findByText("Create your account");
    // The code is cleared from the address bar (and history) once read.
    await waitFor(() => expect(location.hash).toBe(""));
    expect(location.pathname).toBe("/setup");
    fill("owner", "a-good-long-password", "a-good-long-password");
    await waitFor(() => expect(redirect).toHaveBeenCalledWith(`${location.origin}/app`));
    const post = (fetchMock.mock.calls as unknown as Call[]).find(([url]) => url === "/auth/setup");
    expect(post).toBeDefined();
    expect(JSON.parse(String(post![1]!.body))).toEqual({
      code: "the-code",
      provider_subject: "owner",
      password: "a-good-long-password",
    });
  });

  it("needs no code where core allows local setup, and checks the confirmation first", async () => {
    const fetchMock = fetchFor("open");
    vi.stubGlobal("fetch", fetchMock);
    render(<Setup />);
    await screen.findByText("Create your account");
    fill("owner", "a-good-long-password", "a-different-password");
    expect(await screen.findByText("Password and confirmation don't match.")).toBeInTheDocument();
    expect((fetchMock.mock.calls as unknown as Call[]).some(([url]) => url === "/auth/setup")).toBe(false);
  });

  it("shows a refused code in plain words", async () => {
    window.history.pushState({}, "", "/setup#code=old");
    vi.stubGlobal("fetch", fetchFor("code", { status: 403, tag: "code" }));
    render(<Setup />);
    await screen.findByText("Create your account");
    fill("owner", "a-good-long-password", "a-good-long-password");
    expect(await screen.findByText(/This setup link is not valid/)).toBeInTheDocument();
  });

  it("says setup is done once an account exists", async () => {
    vi.stubGlobal("fetch", fetchFor(undefined));
    render(<Setup />);
    expect(await screen.findByText("Already set up")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
  });
});

describe("setupCodeFrom", () => {
  it("reads the code from the fragment only", async () => {
    const { setupCodeFrom } = await import("./Setup");
    expect(setupCodeFrom("#code=abc_DEF-1")).toBe("abc_DEF-1");
    expect(setupCodeFrom("")).toBe("");
    expect(setupCodeFrom("#other=1")).toBe("");
  });

  it("does not take a code from the query string", async () => {
    window.history.pushState({}, "", "/setup?code=from-query");
    vi.stubGlobal("fetch", fetchFor("code"));
    render(<Setup />);
    expect(await screen.findByText(/Open the setup link from the installer's output/)).toBeInTheDocument();
    vi.unstubAllGlobals();
  });
});

describe("setupErrorFor", () => {
  it("puts each refusal by its field", () => {
    expect(setupErrorFor(400, "username", "that username is taken")).toEqual({ field: "username", text: "that username is taken" });
    expect(setupErrorFor(400, "password", null).field).toBe("password");
    expect(setupErrorFor(403, "code", null).text).toMatch(/fresh one from the installer/);
    expect(setupErrorFor(409, "complete", null).text).toMatch(/already set up/);
    expect(setupErrorFor(429, null, null).text).toMatch(/Too many attempts/);
    expect(setupErrorFor(500, null, null).text).toBe("Could not create the account (500).");
  });
});
