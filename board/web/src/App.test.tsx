import { describe, it, expect, vi, beforeEach } from "vitest";
import { useEffect } from "react";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { api, ApiError } from "./api";
import { Shell, Boards } from "./App";
import { MeProvider } from "./me";
import { markSignInStuck } from "./signIn";

const responses: Record<string, unknown> = {};
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return { ...actual, api: vi.fn((path: string) => Promise.resolve(responses[path] ?? [])) };
});

const { renewSilently } = vi.hoisted(() => ({
  renewSilently: vi.fn(() => Promise.resolve("token" as const)),
}));
vi.mock("./authClient", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./authClient")>();
  return { ...actual, renewSilently, startTokenRenewal: vi.fn() };
});

// Shell renders ThemeToggle, which reads prefers-color-scheme via
// matchMedia — jsdom doesn't implement it.
window.matchMedia ??= ((query: string) => ({
  matches: false,
  media: query,
  onchange: null,
  addListener: () => {},
  removeListener: () => {},
  addEventListener: () => {},
  removeEventListener: () => {},
  dispatchEvent: () => false,
})) as unknown as typeof window.matchMedia;

describe("Shell nav", () => {
  beforeEach(() => { for (const k of Object.keys(responses)) delete responses[k]; });
  it("hides the Tokens link from members", async () => {
    responses["/api/me"] = { kind: "service", is_admin: false, is_human: true };
    render(<MemoryRouter><Shell><div /></Shell></MemoryRouter>);
    await screen.findByText("Boards");
    expect(screen.queryByText("Tokens")).toBeNull();
  });
  it("shows the release version and commit beside the Board title", async () => {
    responses["/api/me"] = { kind: "service", is_admin: false, is_human: true };
    responses["/api/version"] = { version: "510d0c0", release: "0.2.0", commit: "510d0c0" };
    render(<MemoryRouter><Shell><div /></Shell></MemoryRouter>);
    const el = await screen.findByText(/v0\.2\.0/);
    expect(el).toHaveTextContent("v0.2.0 · 510d0c0");
    expect(el).toHaveAttribute("title", expect.stringContaining("release 0.2.0"));
  });
  it("links to a newer release only when /api/version reports one", async () => {
    responses["/api/me"] = { kind: "service", is_admin: false, is_human: true };
    responses["/api/version"] = { version: "x", release: "0.4.0", commit: "x" };
    const first = render(<MemoryRouter><Shell><div /></Shell></MemoryRouter>);
    await screen.findByText(/v0\.4\.0/);
    expect(document.querySelector(".build-update")).toBeNull();
    first.unmount();
    responses["/api/version"] = { version: "x", release: "0.4.0", commit: "x", latest_version: "0.4.1", latest_url: "https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1" };
    render(<MemoryRouter><Shell><div /></Shell></MemoryRouter>);
    const link = await screen.findByRole("link", { name: /v0\.4\.1 available/ });
    expect(link).toHaveAttribute("href", "https://github.com/blerglab/blerg-ai/releases/tag/v0.4.1");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
    expect(link).toHaveAttribute("title", "A newer Blerg release is available");
  });
  it("shows no version when /api/version fails, rather than an ellipsis", async () => {
    responses["/api/me"] = { kind: "service", is_admin: false, is_human: true };
    const mock = vi.mocked(api);
    const impl = mock.getMockImplementation()!;
    mock.mockImplementation((path: string) => (path === "/api/version" ? Promise.reject(new Error("down")) : impl(path)));
    render(<MemoryRouter><Shell><div /></Shell></MemoryRouter>);
    await screen.findByText("Boards");
    await waitFor(() => expect(mock).toHaveBeenCalledWith("/api/version"));
    expect(screen.queryByText("…")).toBeNull();
    expect(document.querySelector(".build-version")).toBeNull();
    mock.mockImplementation(impl);
  });
  it("reloads its data, without a page reload, when the sign-in banner's quiet retry gets a token", async () => {
    responses["/api/me"] = { kind: "service", is_admin: false, is_human: true };
    const mock = vi.mocked(api);
    const Probe = () => {
      useEffect(() => { void api("/api/probe"); }, []);
      return <div>probe</div>;
    };
    render(<MemoryRouter><Shell><Probe /></Shell></MemoryRouter>);
    await screen.findByText("probe");
    const calls = (path: string) => mock.mock.calls.filter((c) => c[0] === path).length;
    const before = { me: calls("/api/me"), probe: calls("/api/probe") };
    act(() => markSignInStuck());
    expect(screen.getByTestId("sign-in-banner")).toBeInTheDocument();
    await act(async () => {
      window.dispatchEvent(new Event("online"));
    });
    await waitFor(() => expect(screen.queryByTestId("sign-in-banner")).toBeNull());
    expect(renewSilently).toHaveBeenCalled();
    await waitFor(() => expect(calls("/api/probe")).toBe(before.probe + 1));
    expect(calls("/api/me")).toBe(before.me + 1);
  });
  it("shows the Tokens link to admins", async () => {
    responses["/api/me"] = { kind: "service", is_admin: true, is_human: true };
    render(<MemoryRouter><Shell><div /></Shell></MemoryRouter>);
    expect(await screen.findByText("Tokens")).toBeInTheDocument();
  });
});

describe("Boards empty state", () => {
  it("tells a member an admin must mint a token", async () => {
    responses["/api/me"] = { kind: "service", is_admin: false, is_human: true };
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    expect(await screen.findByText(
      "No boards yet — create one. To let agents work here, an admin mints a token on the Tokens page.",
    )).toBeInTheDocument();
  });
  it("tells an admin to mint the token themselves", async () => {
    responses["/api/me"] = { kind: "service", is_admin: true, is_human: true };
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    expect(await screen.findByText(
      "Create one, then mint an agent token so sessions can start stacking cards.",
    )).toBeInTheDocument();
  });
});

describe("New board template", () => {
  beforeEach(() => {
    for (const k of Object.keys(responses)) delete responses[k];
    vi.mocked(api).mockClear();
    responses["/api/me"] = { kind: "service", is_admin: true, is_human: true };
  });
  const postedBody = () => {
    const call = vi.mocked(api).mock.calls.find((c) => c[0] === "/api/boards" && (c[1] as { json?: unknown })?.json);
    return (call?.[1] as { json: Record<string, unknown> }).json;
  };

  it("offers None and Focus board, with a one-line description", async () => {
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    fireEvent.click(await screen.findByText("New board"));
    const select = screen.getByLabelText("Template") as HTMLSelectElement;
    expect(Array.from(select.options).map((o) => o.textContent)).toEqual(["None", "Focus board"]);
    expect(select.value).toBe("");
    fireEvent.change(select, { target: { value: "focus-board" } });
    expect(screen.getByText(/Inbox, Today, This week/)).toBeInTheDocument();
  });

  it("sends no template by default", async () => {
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    fireEvent.click(await screen.findByText("New board"));
    fireEvent.change(screen.getByPlaceholderText("board name"), { target: { value: "plain" } });
    fireEvent.click(screen.getByText("Create board"));
    await waitFor(() => expect(postedBody()).toBeDefined());
    expect(postedBody()).toEqual({ name: "plain", require_repo: false });
  });

  it("sends the chosen template", async () => {
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    fireEvent.click(await screen.findByText("New board"));
    fireEvent.change(screen.getByPlaceholderText("board name"), { target: { value: "me" } });
    fireEvent.change(screen.getByLabelText("Template"), { target: { value: "focus-board" } });
    fireEvent.click(screen.getByText("Create board"));
    await waitFor(() => expect(postedBody()).toBeDefined());
    expect(postedBody()).toEqual({ name: "me", require_repo: false, template: "focus-board" });
  });

  it("shows the server's error in the form and keeps what was typed when creating fails", async () => {
    const real = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation((path: string, init?: unknown) =>
      path === "/api/boards" && (init as { json?: unknown })?.json
        ? Promise.reject(new ApiError(400, { error: "template cannot be combined with columns or field_schema" }))
        : real(path, init as never));
    try {
      render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
      fireEvent.click(await screen.findByText("New board"));
      fireEvent.change(screen.getByPlaceholderText("board name"), { target: { value: "me" } });
      fireEvent.change(screen.getByLabelText("Template"), { target: { value: "focus-board" } });
      fireEvent.click(screen.getByText("Create board"));
      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent("template cannot be combined with columns or field_schema");
      // the form stays open with its input, and is usable again
      expect((screen.getByPlaceholderText("board name") as HTMLInputElement).value).toBe("me");
      expect((screen.getByLabelText("Template") as HTMLSelectElement).value).toBe("focus-board");
      expect(screen.getByText("Create board")).not.toBeDisabled();
    } finally {
      vi.mocked(api).mockImplementation(real);
    }
  });

  it("clears the error and closes the form when a retry succeeds", async () => {
    const real = vi.mocked(api).getMockImplementation()!;
    let fail = true;
    vi.mocked(api).mockImplementation((path: string, init?: unknown) =>
      path === "/api/boards" && (init as { json?: unknown })?.json && fail
        ? Promise.reject(new ApiError(500, { error: "boom" }))
        : real(path, init as never));
    try {
      render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
      fireEvent.click(await screen.findByText("New board"));
      fireEvent.change(screen.getByPlaceholderText("board name"), { target: { value: "me" } });
      fireEvent.click(screen.getByText("Create board"));
      await screen.findByRole("alert");
      fail = false;
      fireEvent.click(screen.getByText("Create board"));
      await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
      expect(screen.queryByPlaceholderText("board name")).toBeNull(); // form closed on success
    } finally {
      vi.mocked(api).mockImplementation(real);
    }
  });

  it("pre-checks Run this board's cards as me and sends it", async () => {
    responses["/api/me"] = { kind: "human", is_admin: true, is_human: true, run_as_me_available: true, run_as_creator_default: true };
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    fireEvent.click(await screen.findByText("New board"));
    const box = await screen.findByLabelText("Run this board's cards as me") as HTMLInputElement;
    expect(box.checked).toBe(true);
    fireEvent.change(screen.getByPlaceholderText("board name"), { target: { value: "mine" } });
    fireEvent.click(screen.getByText("Create board"));
    await waitFor(() => expect(postedBody()).toBeDefined());
    expect(postedBody()).toEqual({ name: "mine", require_repo: false, run_as_me: true });
  });

  it("leaves the box unchecked when the operator turns the default off, and honors a change", async () => {
    responses["/api/me"] = { kind: "human", is_admin: true, is_human: true, run_as_me_available: true, run_as_creator_default: false };
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    fireEvent.click(await screen.findByText("New board"));
    const box = await screen.findByLabelText("Run this board's cards as me") as HTMLInputElement;
    expect(box.checked).toBe(false);
    fireEvent.change(screen.getByPlaceholderText("board name"), { target: { value: "team" } });
    fireEvent.click(screen.getByText("Create board"));
    await waitFor(() => expect(postedBody()).toBeDefined());
    expect(postedBody()).toEqual({ name: "team", require_repo: false, run_as_me: false });
  });

  it("has no Run as me box when the install cannot mint", async () => {
    responses["/api/me"] = { kind: "human", is_admin: true, is_human: true, run_as_me_available: false, run_as_creator_default: true };
    render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
    fireEvent.click(await screen.findByText("New board"));
    await screen.findByPlaceholderText("board name");
    expect(screen.queryByLabelText("Run this board's cards as me")).toBeNull();
  });

  it("says when the board was created but Run as me did not take", async () => {
    responses["/api/me"] = { kind: "human", is_admin: true, is_human: true, run_as_me_available: true, run_as_creator_default: true };
    const real = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation((path: string, init?: unknown) =>
      path === "/api/boards" && (init as { json?: unknown })?.json
        ? Promise.resolve({ id: "b9", name: "mine", automation_token_set: false, automation_error: "blerg-core did not accept your sign-in as live: sign in again and retry" })
        : real(path, init as never));
    try {
      render(<MemoryRouter><MeProvider><Boards /></MeProvider></MemoryRouter>);
      fireEvent.click(await screen.findByText("New board"));
      fireEvent.change(screen.getByPlaceholderText("board name"), { target: { value: "mine" } });
      fireEvent.click(screen.getByText("Create board"));
      expect(await screen.findByRole("status")).toHaveTextContent(/created, but it can't start sessions yet: blerg-core did not accept your sign-in/);
    } finally {
      vi.mocked(api).mockImplementation(real);
    }
  });

  it("opens the create form with the template chosen from ?new=focus-board", async () => {
    render(<MemoryRouter initialEntries={["/?new=focus-board"]}><MeProvider><Boards /></MeProvider></MemoryRouter>);
    const select = (await screen.findByLabelText("Template")) as HTMLSelectElement;
    expect(select.value).toBe("focus-board");
  });
});

describe("wordmark link", () => {
  it("keeps a port (via coreOrigin(), not the old hostname-only derivation)", async () => {
    responses["/api/me"] = { kind: "service", is_admin: true, is_human: true };
    render(<MemoryRouter><Shell><div /></Shell></MemoryRouter>);
    await screen.findByText("Boards");
    const link = screen.getByTitle("blerg control plane") as HTMLAnchorElement;
    // jsdom's default test origin is http://localhost:3000 — a bare host
    // (no dots), so coreOrigin() takes the desktop branch and swaps in
    // core's own published port (default 8081). The old
    // `${protocol}//${hostname}` derivation dropped the port entirely,
    // producing a dead http://localhost/ link — this pins that it no
    // longer does.
    expect(link.href).not.toBe("http://localhost/");
    expect(link.href).toContain(":8081");
  });
});
