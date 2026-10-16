import { describe, expect, it } from "vitest";
import type { AgentEvent, UserMessagePayload } from "@blerglab/chat";
import { describeUserMessage } from "./SessionChat";

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
