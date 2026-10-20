import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import Login from "./Login";

describe("Login", () => {
  afterEach(() => vi.unstubAllGlobals());
  it("offers the sign-in form once the install is set up, with no word of a bootstrap password", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ id: "local" }) })));
    render(<Login />);
    expect(await screen.findByText("First time here? Ask an administrator of this Blerg for an account.")).toBeInTheDocument();
    expect(screen.getByLabelText("Username")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("BLERG_BOOTSTRAP_ADMIN_PASSWORD");
    // The internal column name never reaches a person.
    expect(document.body.textContent).not.toContain("provider_subject");
  });

  it("tells a person on an install that is not set up yet to open the setup link", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ id: "local", setup: "code" }) })));
    render(<Login />);
    expect(
      await screen.findByText(
        "This Blerg isn't set up yet — open the setup link from the installer output to create the first account.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Username")).toBeNull();
  });

  it("goes on to /setup when this browser can create the first account", async () => {
    const replace = vi.fn();
    vi.stubGlobal("location", { ...window.location, search: "", replace });
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve({ ok: true, json: () => Promise.resolve({ id: "local", setup: "open" }) })));
    render(<Login />);
    await waitFor(() => expect(replace).toHaveBeenCalledWith("/setup"));
    expect(screen.queryByLabelText("Username")).toBeNull();
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
