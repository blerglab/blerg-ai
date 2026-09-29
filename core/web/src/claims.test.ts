import { describe, it, expect } from "vitest";
import { tokenCaps, mustChangePassword } from "./claims";

// b64url encodes a compact-JWT segment the way core's own signer does (base64url, no padding).
function b64url(obj: unknown): string {
  return btoa(JSON.stringify(obj)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fakeToken(claims: unknown): string {
  return `${b64url({ alg: "EdDSA" })}.${b64url(claims)}.sig`;
}

describe("tokenCaps", () => {
  it("reads the caps claim out of a well-formed token", () => {
    expect(tokenCaps(fakeToken({ caps: ["card.read", "card.write"] }))).toEqual([
      "card.read",
      "card.write",
    ]);
  });

  it("returns [] for a token with no caps claim", () => {
    expect(tokenCaps(fakeToken({ sub: "u1" }))).toEqual([]);
  });

  it("returns [] for a non-array caps claim", () => {
    expect(tokenCaps(fakeToken({ caps: "not-an-array" }))).toEqual([]);
  });

  it("filters out non-string entries in caps", () => {
    expect(tokenCaps(fakeToken({ caps: ["card.read", 42, null] }))).toEqual(["card.read"]);
  });

  it("returns [] for garbage input", () => {
    expect(tokenCaps("not-a-jwt-at-all")).toEqual([]);
    expect(tokenCaps("")).toEqual([]);
  });
});

describe("mustChangePassword", () => {
  it("is false for null", () => {
    expect(mustChangePassword(null)).toBe(false);
  });

  it("is true for a password.change-only token", () => {
    expect(mustChangePassword(fakeToken({ caps: ["password.change"] }))).toBe(true);
  });

  it("is false once the token also carries card.read (post-change, or never gated)", () => {
    expect(mustChangePassword(fakeToken({ caps: ["password.change", "card.read"] }))).toBe(false);
  });

  it("is false for a normal member token", () => {
    expect(
      mustChangePassword(fakeToken({ caps: ["card.read", "card.write", "column.write"] })),
    ).toBe(false);
  });
});
