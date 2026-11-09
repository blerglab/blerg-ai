import { afterEach, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { CardRelease, ReleasePlanning } from "./ReleasePlanning";
import { api, type Card } from "./api";
vi.mock("./api", () => ({ api: vi.fn(), pendingBlockers: () => [] }));
afterEach(() => { cleanup(); vi.resetAllMocks(); });
it("assigns and clears a card release with its version", async () => {
 const card = { id: "c1", board_id: "b1", release_id: "r1", version: 7 } as Card;
 vi.mocked(api).mockResolvedValueOnce([{ id: "r1", name: "First", status: "planned" }]);
 const onChange = vi.fn(); render(<CardRelease card={card} onChange={onChange} />);
 await screen.findByRole("option", { name: "First" });
 vi.mocked(api).mockResolvedValueOnce({ ...card, release_id: null, version: 8 });
 fireEvent.change(screen.getByLabelText("Card release"), { target: { value: "" } });
 await waitFor(() => expect(onChange).toHaveBeenCalledWith({ ...card, release_id: null, version: 8 }));
 expect(api).toHaveBeenLastCalledWith("/api/cards/c1", { method: "PATCH", body: JSON.stringify({ release_id: "", if_match: 7 }) });
});
it("shows a closed card's release and keeps it assigned", async () => {
 vi.mocked(api).mockResolvedValue([{ id: "r1", name: "Shipped", status: "released" }]);
 render(<CardRelease card={{ id: "c1", board_id: "b1", release_id: "r1" } as Card} onChange={vi.fn()} />);
 await screen.findByRole("option", { name: "Shipped" });
 expect(screen.getByLabelText("Card release")).toBeDisabled();
});
it("creates a planned release and groups unassigned cards", async () => {
 vi.mocked(api).mockResolvedValue([]);
 const onGroup = vi.fn();
 render(<ReleasePlanning boardId="b1" readOnly={false} selected="" onFilter={vi.fn()} grouped={false} onGroup={onGroup} onOpenCard={vi.fn()} />);
 fireEvent.click(screen.getByRole("button", { name: "Releases" }));
 fireEvent.change(screen.getByLabelText("Release name"), { target: { value: "First" } });
 fireEvent.change(screen.getByLabelText("Target date"), { target: { value: "2026-12-01" } });
 fireEvent.click(screen.getByRole("button", { name: "Add release" }));
 await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1/releases", { method: "POST", body: JSON.stringify({ name: "First", target_date: "2026-12-01", status: "planned" }) }));
 fireEvent.click(screen.getByRole("button", { name: "Group by release" }));expect(onGroup).toHaveBeenCalledWith(true);
});
it("requires confirmation before cutting", async () => {
 vi.mocked(api).mockImplementation(async path => path.endsWith('/releases/r1') ? {release:{id:'r1',name:'First',status:'planned'},cards:[],done:0,by_column:{}} : [{id:'r1',name:'First',status:'planned'}]);
 render(<ReleasePlanning boardId="b1" readOnly={false} selected="" onFilter={vi.fn()} grouped={false} onGroup={vi.fn()} onOpenCard={vi.fn()} />);
 fireEvent.click(screen.getByRole('button',{name:'Releases'}));
 fireEvent.click(await screen.findByRole('button',{name:'First'}));
 fireEvent.click(await screen.findByRole('button',{name:'Cut release'}));
 expect(screen.getByRole('button',{name:'Confirm cut'})).toBeInTheDocument();
 expect(vi.mocked(api).mock.calls.some(([,opts])=>opts?.method==='POST')).toBe(false);
});
it("confirms only Done cards that still need archiving", async () => {
 const release={id:'r1',name:'First',status:'planned'};
 vi.mocked(api).mockImplementation(async path=>path.endsWith('/releases/r1') ? {release,cards:[{id:'c1',number:1,title:'Already archived',archived_at:'2026-11-01',blockers:[]}],done:1,done_card_ids:['c1'],by_column:{Done:1}} : [release]);
 render(<ReleasePlanning boardId="b1" readOnly={false} selected="" onFilter={vi.fn()} grouped={false} onGroup={vi.fn()} onOpenCard={vi.fn()} />);
 fireEvent.click(screen.getByRole('button',{name:'Releases'}));
 fireEvent.click(await screen.findByRole('button',{name:'First'}));
 fireEvent.click(await screen.findByRole('button',{name:'Cut release'}));
 expect(screen.getByRole('alertdialog')).toHaveTextContent('Archive 0 Done cards');
});
