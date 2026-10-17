import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SessionModelControls } from "./SessionModelControls";
import { api } from "./api";
import type { RunnerSession } from "./SessionChat";
vi.mock("./api", () => ({ api: vi.fn() }));
vi.mock("./me", () => ({ useMe: () => ({ is_human: true }) }));
const session: RunnerSession = { id: "fixture-session", board_id: "b1", runner: "blerg", external_session_id: "external", lifecycle: "idle", role: "board", resumable: false, created_at: "", engine: "codex", model: "gpt-current", effort: "low" };
beforeEach(() => {
  vi.mocked(api).mockReset();
  vi.mocked(api).mockImplementation(async path => path.includes("launch-options") ? { state: "known", engines: [{ id: "codex", name: "Codex", models: { models: [{ id: "gpt-current", efforts: ["low", "high"] }, { id: "gpt-next", efforts: ["high"] }] } }, { id: "claude", name: "Claude", models: { models: [{ id: "claude-fixture", efforts: [] }] } }] } as never : {} as never);
});
describe("native session model controls", () => {
  it("uses only its native engine models and clears effort on model change", async () => {
    render(<SessionModelControls session={session} />);
    const model = await screen.findByRole("combobox", { name: "Session model" });
    expect(screen.queryByRole("option", { name: "claude-fixture" })).toBeNull();
    expect(screen.queryByRole("combobox", { name: "Engine" })).toBeNull();
    fireEvent.change(model, { target: { value: "gpt-next" } });
    expect(screen.getByRole("combobox", { name: "Session effort" })).toHaveValue("");
    fireEvent.change(screen.getByRole("combobox", { name: "Session effort" }), { target: { value: "high" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply settings" }));
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/runner-sessions/fixture-session/model", { json: { model: "gpt-next", effort: "high" } }));
    expect(await screen.findByRole("status")).toHaveTextContent("requested for the next turn");
  });
  it("reports rejected controls instead of claiming success", async () => {
    render(<SessionModelControls session={session} />);
    await screen.findByRole("combobox", { name: "Session model" });
    vi.mocked(api).mockRejectedValue(new Error("Session host unavailable"));
    fireEvent.click(screen.getByRole("button", { name: "Apply settings" }));
    expect(await screen.findByRole("status")).toHaveTextContent("Session host unavailable");
  });
  it("does not offer controls on disconnected sessions", () => {
    render(<SessionModelControls session={{ ...session, lifecycle: "disconnected", resumable: true }} />);
    expect(api).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Apply settings" })).toBeNull();
  });
});
