import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { BoardChat } from "./BoardChat";
import type { RunnerSession } from "./SessionChat";
vi.mock("./api", () => ({ api: vi.fn() }));
vi.mock("./SessionChat", () => ({ SessionChat: ({ readOnly }: { readOnly?: boolean }) => <div data-testid="chat">{readOnly ? "read only" : "can message"}</div> }));
const session: RunnerSession = { id: "fixture-session", runner: "blerg", external_session_id: "external", lifecycle: "disconnected", role: "board", resumable: true, resume_mode: "native", created_at: "" };
beforeEach(() => { vi.mocked(api).mockReset(); sessionStorage.clear(); });
describe("BoardChat recovery", () => {
  it("allows a message to resume an eligible native conversation without creating another session", async () => {
    vi.mocked(api).mockResolvedValue([session]);
    render(<BoardChat boardId="b1" onClosed={vi.fn()} />);
    expect(await screen.findByTestId("chat")).toHaveTextContent("can message");
    expect(screen.getByText("Send a message to resume the retained conversation.")).toBeInTheDocument();
    expect(api).toHaveBeenCalledTimes(1);
  });
  it("discloses transcript reconstruction rather than promising native identity", async () => {
    vi.mocked(api).mockResolvedValue([{ ...session, resume_mode: "transcript_replay" }]);
    render(<BoardChat boardId="b1" onClosed={vi.fn()} />);
    expect(await screen.findByTestId("chat")).toHaveTextContent("can message");
    expect(screen.getByText(/reconstructed transcript context/)).toBeInTheDocument();
  });
  it("shows the backend refusal and keeps an ineligible conversation read only", async () => {
    vi.mocked(api).mockResolvedValue([{ ...session, resumable: false, resume_reason: "Retained history is unavailable" }]);
    render(<BoardChat boardId="b1" onClosed={vi.fn()} />);
    expect(await screen.findByTestId("chat")).toHaveTextContent("read only");
    expect(screen.getByText("Retained history is unavailable")).toBeInTheDocument();
    expect(api).toHaveBeenCalledTimes(1);
  });
});

describe("BoardChat start failure", () => {
  it("says why the session could not start, opens board settings and tries again", async () => {
    const reason = "app is a desktop folder name, not owner/name, and none of the connected desktops has a folder named app, so it can't run anywhere: open Blerg on the desktop that has it, or resolve it under Repos in board settings";
    vi.mocked(api)
      .mockResolvedValueOnce([])
      .mockRejectedValueOnce(new Error(reason))
      .mockResolvedValueOnce([])
      .mockResolvedValueOnce({})
      .mockResolvedValueOnce([{ ...session, lifecycle: "starting" }]);
    render(<BoardChat boardId="b1" onClosed={vi.fn()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(`The board session could not start: ${reason}`);
    const opened = vi.fn();
    window.addEventListener("blerg-board:open-settings", opened);
    fireEvent.click(screen.getByRole("button", { name: "Open board settings" }));
    window.removeEventListener("blerg-board:open-settings", opened);
    expect(opened).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByTestId("chat")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("BoardChat runner refusal", () => {
  it("shows a runner refusal plainly with both actions", async () => {
    const reason = "the runner could not start this session: Desktop has no repository named blerg; open or clone it there, or choose another desktop";
    vi.mocked(api).mockResolvedValueOnce([]).mockRejectedValueOnce(new Error(reason));
    render(<BoardChat boardId="b1" onClosed={vi.fn()} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(`The board session could not start: ${reason}`);
    expect(screen.getByRole("button", { name: "Open board settings" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});
