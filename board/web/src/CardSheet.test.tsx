import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { ReviewSection } from "./CardSheet";
import { MeProvider } from "./me";
import { Card, CardEvent, Column } from "./api";
import { Checks } from "./Checks";

const responses: Record<string, unknown> = {};
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return { ...actual, api: vi.fn((path: string) => Promise.resolve(responses[path] ?? [])) };
});

const card: Card = {
  id: "c1", board_id: "b1", number: 1, rank: "a", column_id: "col-review",
  type: "feature", title: "a card", body: "", priority: "normal", size: null,
  model: "", fields: {}, dedup_key: null, external_id: null, archived_at: null,
  version: 1, gate_flag: null, auto_merge: false, merged_sha: null, merged_at: null,
  run_attempts: 0, stuck_at: null, stale_at: null,
  created_at: "2026-01-01T00:00:00Z", updated_at: "2026-01-01T00:00:00Z",
  repos: [], tags: null, links: null, depends_on: null, blockers: null,
};
const columns: Column[] = [
  { id: "col-review", board_id: "b1", rank: "a", name: "review", is_terminal: false },
  { id: "col-done", board_id: "b1", rank: "b", name: "done", is_terminal: true },
];
const events: CardEvent[] = [];
const checks: Checks = { tests: null, coverage: null, lint: null, version: null, migrations: [] };

function renderSection(me: unknown) {
  responses["/api/me"] = me;
  return render(
    <MemoryRouter>
      <MeProvider>
        <ReviewSection
          card={card} events={events} columns={columns} deployURL={null}
          checks={checks} onDone={() => {}}
        />
      </MeProvider>
    </MemoryRouter>,
  );
}

describe("ReviewSection Accept/Reject gating", () => {
  beforeEach(() => { for (const k of Object.keys(responses)) delete responses[k]; });

  it("hides Accept/Reject from a member", async () => {
    renderSection({ kind: "service", is_admin: false, is_human: true });
    expect(await screen.findByText("Only a board admin can accept or reject this card.")).toBeInTheDocument();
    expect(screen.queryByText("Accept")).toBeNull();
    expect(screen.queryByText("Reject with feedback")).toBeNull();
  });

  it("shows Accept/Reject to an admin", async () => {
    renderSection({ kind: "service", is_admin: true, is_human: true });
    expect(await screen.findByText("Accept")).toBeInTheDocument();
    expect(screen.getByText("Reject with feedback")).toBeInTheDocument();
  });
});
