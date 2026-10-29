import { describe, expect, it, vi } from "vitest";

// Review of #306: the chat keeps rendered Markdown per signed-in account; the board renews tokens
// without the runner's events, so it forgets the kept results when the token names another account.
const forgetMarkdown = vi.fn();
vi.mock("@blerglab/chat", async (original) => ({ ...(await original<typeof import("@blerglab/chat")>()), forgetMarkdown }));

describe("board chat Markdown memory", () => {
  it("is forgotten when the account changes, not on a renewal for the same account", async () => {
    const { rememberBoardChatScope } = await import("./SessionChat");
    rememberBoardChatScope("alice");
    rememberBoardChatScope("alice");
    expect(forgetMarkdown).not.toHaveBeenCalled();
    rememberBoardChatScope("bob");
    expect(forgetMarkdown).toHaveBeenCalledTimes(1);
    rememberBoardChatScope("");
    expect(forgetMarkdown).toHaveBeenCalledTimes(2);
  });
});
