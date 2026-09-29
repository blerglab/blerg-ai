import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { Tokens } from "./Tokens";
import { MeProvider } from "./me";

const responses: Record<string, unknown> = {};
const rejects: Record<string, boolean> = {};
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return {
    ...actual,
    api: vi.fn((path: string) =>
      rejects[path] ? Promise.reject(new Error("network blip")) : Promise.resolve(responses[path] ?? [])),
  };
});

function renderTokens() {
  return render(<MeProvider><Tokens /></MeProvider>);
}

describe("Tokens page admin guard", () => {
  beforeEach(() => {
    for (const k of Object.keys(responses)) delete responses[k];
    for (const k of Object.keys(rejects)) delete rejects[k];
  });

  it("hides the mint form from a member", async () => {
    responses["/api/me"] = { kind: "service", is_admin: false, is_human: true };
    renderTokens();
    expect(await screen.findByText("Only a board admin can mint tokens.")).toBeInTheDocument();
    expect(screen.queryByText("Mint token")).toBeNull();
  });

  it("shows the mint form to an admin", async () => {
    responses["/api/me"] = { kind: "service", is_admin: true, is_human: true };
    renderTokens();
    expect(await screen.findByText("Mint token")).toBeInTheDocument();
  });

  it("fails closed when /api/me never resolves (permanent fetch failure)", async () => {
    // MeProvider's .catch(() => setMe(null)) leaves `me` null forever on a
    // permanent /api/me failure — the guard must still hide the page (this
    // is the bug fixed in this round: `me && !me.is_admin` let `me === null`
    // fall through and render the full mint/revoke UI).
    rejects["/api/me"] = true;
    renderTokens();
    await waitFor(() => expect(screen.getByText("Only a board admin can mint tokens.")).toBeInTheDocument());
    expect(screen.queryByText("Mint token")).toBeNull();
  });
});
