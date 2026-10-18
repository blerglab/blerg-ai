import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChatView, type AgentEvent, type Transport, type UserMessagePayload } from "@blerglab/chat";
import { boardMeta, describeUserMessage } from "./SessionChat";

const ev = { type: "agent_event", session_id: "s", client_event_id: "e", ts: "", kind: "user_message", payload: {} } as AgentEvent;
const look = (p: UserMessagePayload, first = false) => describeUserMessage(ev, p, { first });

describe("SessionChat describeUserMessage", () => {
  it("collapses only the session's first message into the brief", () => {
    expect(look({ text: "You are a worker…", source: "chat" }, true)).toEqual({ brief: true });
    expect(look({ text: "a later message from the runner's app", source: "chat" })).toBeNull();
  });

  it("an answer, a note reply and a person's message are the person's", () => {
    expect(look({ text: "yes", source: "ask_answer", message_id: "m1" })).toBeNull();
    expect(look({ text: "[Re: …] ok", source: "note_reply" })).toBeNull();
    expect(look({ text: "please also fix the title", source: "human" })).toBeNull();
  });

  it("the board's own automation speaks as itself", () => {
    expect(look({ text: "review found two problems", source: "blerg-board" })).toEqual({ author: "blerg-board" });
  });
});

describe("SessionChat boardMeta", () => {
  it("fills the start and model from the board's row and keeps the runner's file limits", () => {
    const limits = { max_file_bytes: 100 * 1024 * 1024, max_session_upload_bytes: 500 * 1024 * 1024 };
    expect(boardMeta({ status: "running", started_at: "", limits }, { created_at: "2026-10-16T00:00:00Z", model: "m1" })).toEqual({
      status: "running", started_at: "2026-10-16T00:00:00Z", model: "m1", limits,
    });
    expect(boardMeta({ status: "idle", started_at: "a", model: "runner-model" }, { created_at: "b", model: "m1" })).toEqual({
      status: "idle", started_at: "a", model: "runner-model",
    });
  });
});


it("preserves explicit native steering and effort while retaining advertised file limits", () => {
  const limits = { max_file_bytes: 100 * 1024 * 1024 };
  const row = { created_at: "board-start", model: "board-model", engine: "claude", effort: "high", steering_supported: true };
  expect(boardMeta({ status: "running", started_at: "native-start", model: "native-model", engine: "codex", effort: "", steering_supported: false, limits }, row)).toEqual({
    status: "running", started_at: "native-start", model: "native-model", engine: "codex", effort: "", steering_supported: false, limits,
  });
  expect(boardMeta({ status: "running", started_at: "", limits }, row)).toEqual({
    status: "running", started_at: "board-start", model: "board-model", engine: "claude", effort: "high", steering_supported: true, limits,
  });
});

describe("SessionChat delegation request", () => {
  it("shows a delegation request read-only", async () => {
    const request = {
      request_id: "r1", engine: "codex", runtime: "daemon", daemon_name: "laptop", title: "Review it", prompt: "Read the diff.",
      offered: ["once", "deny"], expires_at: "2099-01-01T00:00:00Z",
    };
    const event = { type: "agent_event", session_id: "s", client_event_id: "d1", seq: 1, ts: "2026-10-17T00:00:00Z", kind: "delegation_request", payload: request } as AgentEvent;
    // The board's transport has no decideDelegation (proxy.ts implements neither).
    const transport: Transport = {
      subscribe: (_id, _opts, h) => {
        h.onConnection(true);
        h.onEvent(event);
        h.onReplayDone({ hasMore: false, lastSeq: 1 } as never);
        return () => {};
      },
      loadOlder: async () => ({ events: [], hasOlder: false } as never),
      session: async () => ({ status: "running", started_at: "2026-10-17T00:00:00Z" }),
      send: async () => ({ queued: false }),
      stop: async () => {},
      connected: () => true,
    };
    render(<ChatView session="s" transport={transport} readOnly />);
    const card = await screen.findByTestId("delegation-card");
    expect(card).toHaveTextContent("This session wants to start a Codex session");
    expect(card).toHaveTextContent("on your desktop, unsandboxed");
    expect(card).toHaveTextContent("Decide in Blerg Runner");
    expect(card.querySelector("button")).toBeNull();
  });
});
