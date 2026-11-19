import { afterEach, expect, it, vi } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { page } from "vitest/browser";
import { ChecklistRow, buildTimeline, type AgentEvent } from "@blerglab/chat";
import { within } from "@testing-library/react";
import { ReleasePlanning } from "./ReleasePlanning";
import { api, type Card } from "./api";
import "./styles.css";
vi.mock("./api", () => ({ api: vi.fn(), boardSocket: () => () => {}, pendingBlockers: (c: Card) => (c.blockers ?? []).filter(b => !b.satisfied) }));
afterEach(() => {cleanup();delete document.documentElement.dataset.theme;});
for (const width of [390, 3840]) {
 it(`plans and cuts a release at ${width}px`, async () => {
  await page.viewport(width, 900);
  const release = { id: "r1", board_id: "b1", name: "First release", status: "planned", target_date: "2026-12-01" };
  const card = { id: "c1", number: 1, title: "Open work", release_id: "r1", column_id: "open", blockers: [{ id: "c2", number: 2, title: "Dependency", satisfied: false }] } as Card;
  vi.mocked(api).mockImplementation(async (path, options) => {
   if (options?.method === "POST") return { ok: true } as never;
   if (path.endsWith("/releases/r1")) return { release, cards: [card], done: 0, by_column: { Open: 1 } } as never;
   if (path.endsWith("/releases")) return [release, { ...release, id: "r2", name: "Next release" }] as never;
   return [card] as never;
  });
  const onFilter = vi.fn(), onGroup = vi.fn(), onOpenCard = vi.fn();
  render(<ReleasePlanning boardId="b1" readOnly={false} selected="" onFilter={onFilter} grouped={false} onGroup={onGroup} onOpenCard={onOpenCard} />);
  await screen.findByRole("option", { name: "First release" });
  fireEvent.change(screen.getByLabelText("Filter by release"), { target: { value: "r1" } });
  expect(onFilter).toHaveBeenCalledWith("r1");
  fireEvent.click(screen.getByRole("button", { name: "Releases" }));
  const selectStyle = getComputedStyle(screen.getByLabelText("Release status"));
  for (const field of [screen.getByLabelText("Release name"), screen.getByLabelText("Target date"), screen.getByRole("textbox", { name: "Milestone tag" })]) {
    const style = getComputedStyle(field);
    expect(style.backgroundColor).toBe(selectStyle.backgroundColor);
    expect(style.borderRadius).toBe(selectStyle.borderRadius);
  }
  fireEvent.click(await screen.findByRole("button", { name: "First release" }));
  await screen.findByText("0 of 1 done");
  expect(screen.getByText(/Blocked/)).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", { name: /#1 Open work/ }));
  expect(onOpenCard).toHaveBeenCalledWith(card);
  if (width === 390) expect(getComputedStyle(screen.getByLabelText("Release name")).fontSize).toBe("16px");
  fireEvent.change(screen.getByLabelText("Next release"), { target: { value: "r2" } });
  fireEvent.click(screen.getByRole("button", { name: "Cut release" }));
  fireEvent.click(screen.getByRole("button", { name: "Confirm cut" }));
  await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1/releases/r1/close", { method: "POST", body: JSON.stringify({ next_release_id: "r2" }) }));
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
 });
}
for (const width of [390, 3840]) {
 it(`groups a long card title without overflow at ${width}px`, async () => {
  await page.viewport(width, 900);
  const card = { id: "c1", number: 1, title: "A".repeat(180), release_id: "r1", blockers: [] } as unknown as Card;
  vi.mocked(api).mockImplementation(async path => (path.endsWith("/releases") ? [{ id: "r1", name: "Release ".repeat(20), status: "planned" }] : [card]) as never);
  render(<ReleasePlanning boardId="b1" readOnly={true} selected="" onFilter={vi.fn()} grouped={true} onGroup={vi.fn()} onOpenCard={vi.fn()} />);
  expect(await screen.findByRole("button", { name: /#1 AAAAA/ })).toBeInTheDocument();
  expect(screen.getByRole("heading", { name: "No release" })).toBeInTheDocument();
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
 });
}
for (const width of [390, 3840]) for (const theme of ["light", "dark"]) {
 it(`shows burn charts and their table without overflow at ${width}px in ${theme}`, async () => {
  await page.viewport(width,900);
  document.documentElement.dataset.theme=theme;
  const point={date:"2026-01-01",scope:3,done:1,confirmed:0,added:3,finished:1};
  vi.mocked(api).mockImplementation(async path => path.endsWith("/releases") ? [{id:"r1",name:"First",status:"planned"}] as never : {release:{id:"r1",name:"First",status:"planned"},cards:[],done:0,by_column:{},burn:{count:[point],size:[point],estimated:true}} as never);
  render(<ReleasePlanning boardId="b1" readOnly={true} selected="" onFilter={vi.fn()} grouped={false} onGroup={vi.fn()} onOpenCard={vi.fn()} />);
  fireEvent.click(screen.getByRole("button",{name:"Releases"}));
  fireEvent.click(await screen.findByRole("button",{name:"First"}));
  expect(await screen.findByRole("img",{name:/Burn-up/})).toBeInTheDocument();
  fireEvent.change(screen.getByLabelText("Chart type"),{target:{value:"down"}});
  expect(screen.getByRole("img",{name:/Burn-down/})).toBeInTheDocument();
  fireEvent.click(screen.getByText("Data table"));
  expect(screen.getByRole("table",{name:"Release progress data"})).toHaveTextContent("2026-01-01");
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
 });
}

for (const width of [390,3840]) {
 it(`expands a compact checklist into historical state at ${width}px`,async()=>{
  await page.viewport(width,900);
  const event=(seq:number,payload:unknown):AgentEvent=>({type:"agent_event",session_id:"s",client_event_id:`e${seq}`,seq,ts:"2026-01-01T00:00:00Z",kind:"checklist",payload});
  const rows=buildTimeline([event(1,{op:"set",items:[{id:"a",text:"Build",done:false},{id:"b",text:"Ship",done:false}]}),event(2,{delta:true,op:"tick",total:2,completed:1,updates:[{id:"a",text:"Build",done:true}]})]);
  const row=rows.at(-1)!; if(row.type!=="event") throw Error("Missing checklist row");
  render(<ChecklistRow ev={row.ev} />);
  fireEvent.click(screen.getByRole("button",{name:/Checklist/}));
  expect(within(screen.getByTestId("checklist-row")).getAllByRole("listitem").map(li=>li.textContent)).toEqual(["☑Build (done)","☐Ship (to do)"]);
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(width);
 });
}
