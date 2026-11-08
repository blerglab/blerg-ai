import { it, expect, vi, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { page } from "vitest/browser";
import { MemoryRouter } from "react-router-dom";
import { ReviewSection } from "./CardSheet";
import { MeProvider } from "./me";
import type { Card, CardEvent, Column } from "./api";
import type { Checks } from "./Checks";
import "./styles.css";
afterEach(cleanup);
vi.mock("./api", async (original) => ({
 ...await original<typeof import("./api")>(),
 api: vi.fn(async (path: string) => path === "/api/me" ? {kind:"service",is_admin:false,is_human:true} : [{id:"ended",external_session_id:"ended-session",lifecycle:"stopped",role:"worker"}]),
}));
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


it.each([390,3840])("explains ended local diffs and wraps branch and PR at %i px", async width => {
 await page.viewport(width,width===390?844:2160);
 const branch="work/"+"long-branch".repeat(30);
 render(<MemoryRouter><MeProvider><div className="sheet" style={{position:"static",width:"100%",boxSizing:"border-box"}}><ReviewSection card={{...card,fields:{branch},links:[{kind:"pr",url:"https://example.com/pull/1",label:"Review PR"}]}} events={events} columns={columns} deployURL={null} checks={checks} onDone={() => {}} /></div></MeProvider></MemoryRouter>);
 expect(await screen.findByText(/Local diffs are unavailable because the workspace has ended/)).toBeVisible();
 expect(screen.getByText(branch)).toBeVisible();
 expect(screen.getByRole("link",{name:"Review PR"})).toBeVisible();
 expect(screen.queryByRole("link",{name:"View diff in blerg-board"})).toBeNull();
 expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
});
