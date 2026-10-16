import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import Login from "./Login";

describe("Login", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("tells a first-time user where the username comes from", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ id: "local" }) })));
    render(<Login />);
    expect(
      await screen.findByText(
        "First time here? Your username is printed next to BLERG_BOOTSTRAP_ADMIN_PASSWORD in core's log (first boot only). You choose your own username when you first change the password.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
    // The internal column name never reaches a person.
    expect(document.body.textContent).not.toContain("provider_subject");
  });
});

describe("Login notice", () => {
  it("explains why the user is back at the form, from the query string", async () => {
    const { noticeFor } = await import("./Login");
    expect(noticeFor("")).toBeNull();
    expect(noticeFor("?return_to=https%3A%2F%2Fboard.example.com%2F&reason=signed_out")).toBeNull();
    expect(noticeFor("?reason=expired")).toMatch(/session expired/);
    expect(noticeFor("?changed=1")).toBe("Password changed — sign in again.");
    expect(noticeFor("?changed=1&user=1")).toBe("Password changed — sign in again with your new username.");
    expect(noticeFor("?changed=1&user=1&return_to=https%3A%2F%2Fboard.example.com%2F")).toBe(
      "Password changed — sign in again with your new username.",
    );
  });
});
