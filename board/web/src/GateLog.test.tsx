import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { GateLog } from "./GateLog";
import { MeProvider } from "./me";
import { Review } from "./api";

const responses: Record<string, unknown> = {};
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return { ...actual, api: vi.fn((path: string) => Promise.resolve(responses[path] ?? [])) };
});

const heldReview: Review = {
  id: "r1", board_id: "b1", submitted_at: "2026-01-01T00:00:00Z", operation: "create_card",
  verdict: null, reason: null, policy_applied: "held", backend: null, model: null,
  latency_ms: null, payload: { title: "a card" }, card_id: null,
  held_expires_at: "2026-01-02T00:00:00Z", target_card_id: null, target_version: null,
};

function renderLog(me: unknown) {
  responses["/api/me"] = me;
  responses["/api/reviews?state=held"] = [heldReview];
  return render(<MeProvider><GateLog /></MeProvider>);
}

describe("GateLog Approve/Reject gating", () => {
  beforeEach(() => { for (const k of Object.keys(responses)) delete responses[k]; });

  it("hides Approve/Reject from a member", async () => {
    renderLog({ kind: "service", is_admin: false, is_human: true });
    await screen.findByText("admin only");
    expect(screen.queryByText("Approve")).toBeNull();
    expect(screen.queryByText("Reject")).toBeNull();
  });

  it("shows Approve/Reject to an admin", async () => {
    renderLog({ kind: "service", is_admin: true, is_human: true });
    expect(await screen.findByText("Approve")).toBeInTheDocument();
    expect(screen.getByText("Reject")).toBeInTheDocument();
  });
});
