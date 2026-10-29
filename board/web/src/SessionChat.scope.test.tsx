// The chat keeps a few things on the device per account (pending attachments, the line wrap
// choice, #265): the Board's transport names the signed-in account, and the next sign-in's.
import { describe, expect, it, vi } from "vitest";

const token = vi.hoisted(() => ({ value: null as string | null }));
vi.mock("./authClient", () => ({ getAccessToken: () => token.value, ensureFreshToken: async () => true }));
import { boardChatScope, boardTransport } from "./SessionChat";

const jwt = (sub: string) => `x.${btoa(JSON.stringify({ sub, exp: 9999999999 }))}.x`;

describe("the Board chat's account scope", () => {
  it("follows the signed-in account through the transport", () => {
    const files = boardTransport().files!;
    token.value = jwt("person-a");
    const a = files.queueScope!();
    expect(a).toContain("person-a");
    token.value = jwt("person-b");
    const b = files.queueScope!();
    expect(b).toContain("person-b");
    expect(b).not.toBe(a);
    token.value = null;
    expect(files.queueScope!()).toBe("");
  });

  it("reads nobody from a missing or broken token", () => {
    expect(boardChatScope(null)).toBe("");
    expect(boardChatScope("not-a-jwt")).toBe("");
    expect(boardChatScope(jwt("person-a"))).toBe("person-a");
  });
});
