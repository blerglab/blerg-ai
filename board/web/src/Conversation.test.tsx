// The board's session panels over the shared chat: the board's own chrome (which session, run,
// discuss, run again, the drawer's close) around a chat that talks to /api/chat with the board's
// bearer token, labels the board's own messages as the board's, and folds the kickoff prompt away.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useTranscriptStore } from "@blerglab/chat";
import { Conversation } from "./Conversation";
import { BoardChat } from "./BoardChat";
import { consumeAccessTokenFromFragment } from "./authClient";
import type { RunnerSession } from "./SessionChat";

const responses: Record<string, unknown> = {};
const apiCalls: Array<{ path: string; init?: unknown }> = [];
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return {
    ...actual,
    api: vi.fn((path: string, init?: unknown) => {
      apiCalls.push({ path, init });
      return Promise.resolve(responses[path] ?? []);
    }),
  };
});

const session = (over: Partial<RunnerSession> = {}): RunnerSession => ({
  id: "rs1", runner: "blerg-runner", external_session_id: "ext-12345678", lifecycle: "running",
  role: "worker", resumable: false, created_at: "2026-10-06T10:00:00Z", model: "claude-sonnet-5", ...over,
});

// A token that has not expired, in the fragment the app reads it from.
function signIn() {
  const jwt = `h.${btoa(JSON.stringify({ exp: Math.floor(Date.now() / 1000) + 600 }))}.s`;
  window.history.replaceState(null, "", `/#access_token=${jwt}`);
  consumeAccessTokenFromFragment();
  return jwt;
}

const frame = (event: string, data: unknown, id?: number) =>
  `${id !== undefined ? `id: ${id}\n` : ""}event: ${event}\ndata: ${JSON.stringify(data)}\n\n`;

const userMsg = (seq: number, text: string, source: string) =>
  frame("agent_event", { seq, ts: "2026-10-06T10:00:01Z", kind: "user_message", payload: { text, source } }, seq);

// The board's chat proxy, as fetch sees it: a status, a live stream that replays the given frames
// and stays open, an empty file list, and a message route that records what was sent.
function chatServer(frames: string[], status = "idle") {
  const seen: Array<{ method: string; url: string; headers: Record<string, string>; body?: string }> = [];
  const impl = vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const url = String(input);
    seen.push({ method: init.method ?? "GET", url, headers: (init.headers ?? {}) as Record<string, string>, body: typeof init.body === "string" ? init.body : undefined });
    if (url.includes("/events/live")) {
      const body = new ReadableStream<Uint8Array>({
        start(ctl) {
          const enc = new TextEncoder();
          ctl.enqueue(enc.encode(frame("status", { status })));
          for (const f of frames) ctl.enqueue(enc.encode(f));
          ctl.enqueue(enc.encode(frame("replay_done", { last_seq: frames.length, has_more: false })));
        },
      });
      return new Response(body, { status: 200, headers: { "Content-Type": "text/event-stream" } });
    }
    if (url.endsWith("/artifacts")) return Response.json({ artifacts: [] });
    if (url.endsWith("/messages")) return Response.json({ ok: true });
    if (/\/sessions\/[^/]+$/.test(url)) return Response.json({ lifecycle: "running", runtime: "cluster" });
    return new Response("no route", { status: 404 });
  });
  vi.stubGlobal("fetch", impl);
  return seen;
}

beforeEach(() => {
  for (const k of Object.keys(responses)) delete responses[k];
  apiCalls.length = 0;
  useTranscriptStore.setState({ sessions: {} });
  Element.prototype.scrollIntoView = () => {};
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Conversation (a card's session)", () => {
  it("offers Run and Discuss when the card has no session, and spawns one", async () => {
    render(<Conversation cardId="c1" />);
    fireEvent.click(await screen.findByRole("button", { name: /Run this card/ }));
    await waitFor(() => expect(apiCalls.some((c) => c.path === "/api/cards/c1/spawn")).toBe(true));
    expect(screen.getByRole("button", { name: /Discuss first/ })).toBeInTheDocument();
  });

  it("shows the session in the shared chat, through the board's proxy with the board's token", async () => {
    const jwt = signIn();
    responses["/api/cards/c1/runner-sessions"] = [session()];
    const seen = chatServer([
      userMsg(1, "You are the worker for card #7. **Rules:** many.", "chat"),
      userMsg(2, "the reviewer found two problems", "blerg-board"),
      userMsg(3, "also fix the title", "human"),
    ]);
    const { container } = render(<Conversation cardId="c1" />);

    // the board's own chrome
    expect(await screen.findByText("running")).toBeInTheDocument();
    expect(screen.getByText(/blerg-runner · ext-1234 · claude-sonnet-5/)).toBeInTheDocument();

    // the shared chat, fed by the board's proxy
    await waitFor(() => expect(screen.getByText("also fix the title")).toBeInTheDocument());
    expect(container.querySelector(".session-chat .agent-chat")).not.toBeNull();
    const live = seen.find((c) => c.url.includes("/events/live"))!;
    expect(live.url.startsWith("/api/chat/sessions/rs1/events/live")).toBe(true);
    expect(live.headers.Authorization).toBe(`Bearer ${jwt}`);

    // the kickoff prompt is a collapsed brief, the board's relay is the board's, a person's is theirs
    expect(screen.getByTestId("message-brief").textContent).toMatch(/session brief/);
    expect(screen.queryByText(/You are the worker/)).toBeNull();
    const authors = [...container.querySelectorAll(".agent-card.user .card-author")].map((n) => n.textContent);
    expect(authors).toEqual(["blerg-board", "You"]);
  });

  it("sends what the person types to the board's message route", async () => {
    signIn();
    responses["/api/cards/c1/runner-sessions"] = [session({ lifecycle: "idle" })];
    const seen = chatServer([userMsg(1, "hello", "human")]);
    render(<Conversation cardId="c1" />);
    const box = await screen.findByPlaceholderText(/steer the session/);
    fireEvent.change(box, { target: { value: "ship it" } });
    await act(async () => { fireEvent.keyDown(box, { key: "Enter" }); });
    await waitFor(() => expect(seen.some((c) => c.url.endsWith("/messages"))).toBe(true));
    const sent = seen.find((c) => c.url.endsWith("/messages"))!;
    expect(sent.method).toBe("POST");
    expect(sent.url).toBe("/api/chat/sessions/rs1/messages");
    expect(JSON.parse(sent.body!)).toEqual({ text: "ship it" });
  });

  it("offers Run again on a session that is over, and Run this card on a discussion", async () => {
    signIn();
    chatServer([], "stopped");
    responses["/api/cards/c1/runner-sessions"] = [session({ lifecycle: "stopped" })];
    const first = render(<Conversation cardId="c1" />);
    expect(await screen.findByRole("button", { name: "Run again" })).toBeInTheDocument();
    first.unmount();

    responses["/api/cards/c1/runner-sessions"] = [session({ id: "rs2", role: "discuss", lifecycle: "idle" })];
    render(<Conversation cardId="c1" />);
    // wait for the session's own line: before it loads, the no-session panel has a Run button too
    expect(await screen.findByText(/discussion · /)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Run this card/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Discuss first/ })).toBeNull();
  });
});

describe("BoardChat (the board session drawer)", () => {
  it("reattaches to the live board session and shows it in the shared chat", async () => {
    signIn();
    responses["/api/boards/b1/sessions"] = [session({ id: "bs1", role: "board", lifecycle: "idle" })];
    const seen = chatServer([userMsg(1, "what is blocked?", "human")]);
    render(<BoardChat boardId="b1" onClosed={() => {}} />);
    expect(await screen.findByText("what is blocked?")).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/ask, triage, file cards/)).toBeInTheDocument();
    expect(seen.some((c) => c.url.startsWith("/api/chat/sessions/bs1/events/live"))).toBe(true);
  });

  it("is read-only, and says why, when the session's pod is gone", async () => {
    signIn();
    responses["/api/boards/b1/sessions"] = [session({ id: "bs1", role: "board", lifecycle: "disconnected" })];
    chatServer([userMsg(1, "earlier question", "human")], "disconnected");
    render(<BoardChat boardId="b1" onClosed={() => {}} />);
    expect(await screen.findByText(/pod is gone/)).toBeInTheDocument();
    expect(await screen.findByText("earlier question")).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("Close ends the session through the board and tells the page", async () => {
    signIn();
    responses["/api/boards/b1/sessions"] = [session({ id: "bs1", role: "board", lifecycle: "idle" })];
    chatServer([]);
    const onClosed = vi.fn();
    render(<BoardChat boardId="b1" onClosed={onClosed} />);
    fireEvent.click(await screen.findByRole("button", { name: /Close/ }));
    await waitFor(() => expect(onClosed).toHaveBeenCalled());
    expect(apiCalls.some((c) => c.path === "/api/runner-sessions/bs1/close")).toBe(true);
  });

  it("minimizes to a dot and restores", async () => {
    signIn();
    sessionStorage.clear();
    responses["/api/boards/b1/sessions"] = [session({ id: "bs1", role: "board", lifecycle: "idle" })];
    chatServer([]);
    render(<BoardChat boardId="b1" onClosed={() => {}} />);
    fireEvent.click(await screen.findByTitle(/minimize/));
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(screen.getByTitle(/click to restore/));
    expect(screen.getByRole("dialog", { name: "Board session" })).toBeInTheDocument();
  });
});
