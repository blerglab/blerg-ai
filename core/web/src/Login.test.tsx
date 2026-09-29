import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import Login from "./Login";

describe("Login", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("tells a first-time user where the username comes from", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ id: "local" }) })));
    render(<Login />);
    expect(await screen.findByText(/Your username is the provider_subject printed next to BLERG_BOOTSTRAP_ADMIN_PASSWORD in core's log/)).toBeInTheDocument();
  });
});

describe("Login notice", () => {
  it("explains why the user is back at the form, from the query string", async () => {
    const { noticeFor } = await import("./Login");
    expect(noticeFor("")).toBeNull();
    expect(noticeFor("?return_to=https%3A%2F%2Fboard.example.com%2F&reason=signed_out")).toBeNull();
    expect(noticeFor("?reason=expired")).toMatch(/session expired/);
    expect(noticeFor("?changed=1")).toMatch(/Password changed/);
  });
});
