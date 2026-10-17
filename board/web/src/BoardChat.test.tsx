import { render, screen } from "@testing-library/react";
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
