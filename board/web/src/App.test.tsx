import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Shell, Boards } from "./App";
import { MeProvider } from "./me";

const responses: Record<string, unknown> = {};
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return { ...actual, api: vi.fn((path: string) => Promise.resolve(responses[path] ?? [])) };
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
