import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { BoardSettings } from "./BoardSettings";
import { MeProvider } from "./me";
import { api, Board } from "./api";

let catalog: unknown = { state: "known", engines: [] };
let me: unknown = { kind: "service", is_admin: true, is_human: true };
let targets: unknown = {};
let runAsMeRefusal: string | null = null;
let remote: unknown = {};
vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return {
    ...actual,
    api: vi.fn((path: string) => {
      if (path.endsWith("/automation/run-as-me") && runAsMeRefusal) {
        return Promise.reject(new actual.ApiError(403, { error: runAsMeRefusal }));
      }
      if (path.includes("/repo-remote?")) return Promise.resolve(remote);
      return Promise.resolve(path === "/api/me" ? me : path.includes("/launch-options") ? catalog : path.endsWith("/run-targets") ? targets : {});
    }),
  };
});

function board(over: Partial<Board> = {}): Board {
  return {
    id: "b1", name: "work", description: null, deploy_url: null, git_base: "",
    model: "", reviewer_model: "", discuss_model: "", chat_model: "",
    field_schema: [], require_repo: true, gate_enabled: false,
    gate_on_unavailable: "open", gate_on_dispute: "open", driven_by: null,
    concurrency: 1, ci_policy: "required",
    automation_engine: "claude", automation_token_set: false, automation_token_expires_at: null,
    automation_account_id: null,
    repos: ["app"], ...over,
  };
}

function open(b: Board, onChanged = vi.fn()) {
  render(<MeProvider><BoardSettings board={b} onChanged={onChanged} /></MeProvider>);
  fireEvent.click(screen.getByRole("button", { name: /model/i }));
  return onChanged;
}

describe("BoardSettings automation", () => {
  beforeEach(() => {
    vi.mocked(api).mockClear();
    catalog = { state: "known", engines: ["claude", "codex", "hermes"].map(id => ({ id, name: id === "claude" ? "Claude" : id, models: { models: [] } })) };
    me = { kind: "service", is_admin: true, is_human: true };
  });

  it.each([
    [{ is_admin: true, board_id: 'b1' }, true],
    [{ is_admin: true, board_id: 'other' }, false],
    [{ is_admin: false }, false],
  ])('shows the confirmation prompt only to an admin for this board: %j', async (viewer, allowed) => {
    me = viewer;
    render(<MeProvider><BoardSettings board={board({ key: '' })} /></MeProvider>);
    await waitFor(() => expect(api).toHaveBeenCalledWith('/api/me'));
    if (allowed) expect(await screen.findByText('· confirm board key')).toBeInTheDocument();
    else expect(screen.queryByText('· confirm board key')).toBeNull();
  });

  it("saves the board runtime and explains an unavailable desktop", async () => {
    me = { is_admin: true, is_human: true, account_id: "owner-fixture" };
    targets = { can_choose: true, default_runtime: "docker", targets: [
      { runtime: "cluster", label: "Cluster" },
      { runtime: "docker", daemon_id: "desktop-a", label: "Desktop · Local sandbox" },
      { runtime: "daemon", daemon_id: "desktop-a", desktop_name: "Workstation", label: "Workstation · This machine" },
      { runtime: "docker", daemon_id: "desktop-offline", label: "Offline desktop", unavailable: "Desktop is disconnected." },
    ] };
    open(board({ automation_token_set: true, automation_account_id: "owner-fixture" }));
    const where = await screen.findByRole("combobox", { name: "Where this board runs" });
    expect(screen.getByRole("option", { name: /Offline desktop.*disconnected/ })).toBeDisabled();
    fireEvent.change(where, { target: { value: "daemon|desktop-a" } });
    // The host needs an explicit acknowledgement that names the machine and who it runs as.
    const ack = screen.getByRole("checkbox", { name: /run on Workstation as the person who runs that desktop.*unattended, without a sandbox/i });
    const saveButton = screen.getByRole("button", { name: "Save where it runs" });
    expect(saveButton).toBeDisabled();
    fireEvent.click(ack);
    fireEvent.click(saveButton);
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1", { method: "PATCH", json: { run_runtime: "daemon", run_daemon_id: "desktop-a", run_host_acknowledged: true } }));
  });

  it("automatically selects a sole discovered engine and hides the engine dropdown", async () => {
    catalog = { state: "known", engines: [{ id: "codex", name: "Codex", models: { models: [] } }] };
    open(board({ automation_token_set: true }));
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1/launch-options", expect.anything()));
    await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1", { method: "PATCH", json: { automation_engine: "codex" } }));
  });

  it("discovers future engines without a hardcoded option list", async () => {
    catalog = { state: "known", engines: [{ id: "codex", name: "Codex", models: { models: [] } }, { id: "fixture-engine", name: "Future Engine", models: { models: [] } }] };
    open(board({ automation_token_set: true }));
    expect(await screen.findByRole("option", { name: "Future Engine" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Claude" })).toBeNull();
  });

  it("saves a first token without inventing an engine before scoped discovery", async () => {
    const onChanged = open(board());
    fireEvent.change(await screen.findByPlaceholderText("paste a run-sessions agent token"), { target: { value: "fixture-token" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(api).toHaveBeenCalledWith("/api/boards/b1", { method: "PATCH", json: { automation_token: "fixture-token" } });
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  it("does not display inherited effort for an explicit role model", () => {
    open(board({ model: "gpt-worker", effort: "high", reviewer_model: "gpt-review", reviewer_effort: "" }));
    expect(screen.getByText(/^gpt-review/).closest("dd")).not.toHaveTextContent("high");
    expect(screen.getAllByText(/^gpt-worker · high/)).toHaveLength(3);
  });

  it("says plainly when the board has no automation token", async () => {
    open(board());
    expect(screen.getByText("no token — this board cannot start sessions")).toBeInTheDocument();
    expect(screen.getByLabelText("no automation token")).toBeInTheDocument();
  });

  it("links to core's Agent tokens and names the run-sessions preset", async () => {
    open(board());
    const link = await screen.findByRole("link", { name: /Settings → Agent tokens/ });
    expect(link.getAttribute("href")).toMatch(/\/settings$/);
    expect(screen.getByText("run-sessions")).toBeInTheDocument();
  });

  it("saves a replacement token first, then refreshes scoped engine choices", async () => {
    const onChanged = open(board({ automation_token_set: true }));
    const input = await screen.findByPlaceholderText("paste a run-sessions agent token");
    fireEvent.change(input, { target: { value: "  tok.en.value  " } });
    fireEvent.change(await screen.findByRole("combobox"), { target: { value: "hermes" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(api).toHaveBeenCalledWith("/api/boards/b1", {
      method: "PATCH", json: { automation_token: "tok.en.value" },
    });
    expect((input as HTMLInputElement).value).toBe("");
    expect((input as HTMLInputElement).type).toBe("password");
  });

  it("changing only the engine never sends a token", async () => {
    open(board({ automation_token_set: true }));
    fireEvent.change(await screen.findByRole("combobox"), { target: { value: "codex" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1", {
      method: "PATCH", json: { automation_engine: "codex" },
    }));
  });

  it("removes the token", async () => {
    open(board({ automation_token_set: true, automation_token_expires_at: new Date(Date.now() + 5 * 86_400_000).toISOString() }));
    expect(screen.getByText("token set")).toBeInTheDocument(); // no account recorded
    expect(screen.getByText(/expires in 4 days|expires in 5 days/)).toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: "Remove token" }));
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1", {
      method: "PATCH", json: { automation_token: "" },
    }));
  });

  it("shows whose token it is, and marks the viewer's own", async () => {
    me = { kind: "service", is_admin: true, is_human: true, account_id: "acct-7" };
    open(board({ automation_token_set: true, automation_account_id: "acct-7" }));
    expect(screen.getByText("account acct-7")).toBeInTheDocument();
    expect(await screen.findByText("(you)")).toBeInTheDocument();
  });

  it("warns that every card-writer can spend and extract the credential", () => {
    open(board({ automation_token_set: true, automation_account_id: "acct-7" }));
    expect(screen.getByText(/Anyone who can write cards on this board can spend this credential/)).toBeInTheDocument();
  });

  it("offers Run as me as the one-click way to give a board a token", async () => {
    me = { kind: "human", is_admin: true, is_human: true, account_id: "acct-7", run_as_me_available: true };
    const onChanged = open(board());
    const button = await screen.findByRole("button", { name: "Run as me" });
    expect(button.closest(".run-as-me-cta")).toHaveClass("prominent");
    // Pasting is the secondary path, out of the way until asked for.
    expect(screen.queryByPlaceholderText("paste a run-sessions agent token")).toBeNull();
    fireEvent.click(button);
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(api).toHaveBeenCalledWith("/api/boards/b1/automation/run-as-me", { method: "POST" });
    // Nothing about a token ever travels through the browser.
    expect(vi.mocked(api).mock.calls.some(([, init]) => JSON.stringify(init ?? {}).includes("automation_token"))).toBe(false);
  });

  it("keeps pasting a token available behind Use a different token…", async () => {
    me = { kind: "human", is_admin: true, is_human: true, account_id: "acct-7", run_as_me_available: true };
    open(board());
    fireEvent.click(await screen.findByRole("button", { name: "Use a different token…" }));
    const input = await screen.findByPlaceholderText("paste a run-sessions agent token");
    fireEvent.change(input, { target: { value: "svc-account-token" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1", { method: "PATCH", json: { automation_token: "svc-account-token" } }));
  });

  it("offers Run as me over someone else's token, not over your own minted one", async () => {
    me = { kind: "human", is_admin: true, is_human: true, account_id: "acct-7", run_as_me_available: true };
    open(board({ automation_token_set: true, automation_account_id: "acct-other" }));
    expect(await screen.findByRole("button", { name: "Run as me" })).toBeInTheDocument();
  });

  it("does not offer Run as me again while the board runs on your live minted token", async () => {
    me = { kind: "human", is_admin: true, is_human: true, account_id: "acct-7", run_as_me_available: true };
    open(board({
      automation_token_set: true, automation_account_id: "acct-7", automation_token_minted: true,
      automation_token_expires_at: new Date(Date.now() + 30 * 86_400_000).toISOString(),
    }));
    expect(await screen.findByText("(you)")).toBeInTheDocument();
    expect(screen.getByText(/Run as me$/)).toBeInTheDocument(); // the "· Run as me" marker
    expect(screen.queryByRole("button", { name: "Run as me" })).toBeNull();
  });

  it("shows why Run as me was refused", async () => {
    me = { kind: "human", is_admin: true, is_human: true, account_id: "acct-7", run_as_me_available: true };
    runAsMeRefusal = "blerg-core did not accept your sign-in as live: sign in again and retry";
    try {
      open(board());
      fireEvent.click(await screen.findByRole("button", { name: "Run as me" }));
      expect(await screen.findByRole("alert")).toHaveTextContent("sign in again");
    } finally {
      runAsMeRefusal = null;
    }
  });

  it("offers no Run as me on an install that cannot mint", async () => {
    me = { kind: "human", is_admin: true, is_human: true, account_id: "acct-7", run_as_me_available: false };
    open(board());
    expect(await screen.findByPlaceholderText("paste a run-sessions agent token")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Run as me" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Use a different token…" })).toBeNull();
  });

  it("hides the form from a non-admin", async () => {
    me = { kind: "service", is_admin: false, is_human: true };
    open(board());
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/me"));
    expect(screen.queryByPlaceholderText("paste a run-sessions agent token")).toBeNull();
    expect(screen.getByText("no token — this board cannot start sessions")).toBeInTheDocument();
  });
});

describe('BoardSettings originating component metadata', () => {
  beforeEach(() => { vi.mocked(api).mockClear(); me = { kind: 'service', is_admin: true, is_human: true }; });
  it('allows an admin to opt this board into version collection', async () => {
    const onChanged = open(board());
    const toggle = await screen.findByRole('checkbox', { name: 'Include component version on filed tickets' });
    expect(toggle).not.toBeChecked();
    fireEvent.click(toggle);
    await waitFor(() => expect(api).toHaveBeenCalledWith('/api/boards/b1', { method: 'PATCH', json: { include_component_version: true } }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });
  it('shows the setting without allowing members to change it', async () => {
    me = { kind: 'service', is_admin: false, is_human: true };
    open(board({ include_component_version: true }));
    expect(await screen.findByText('Component version collection: on')).toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: 'Include component version on filed tickets' })).not.toBeInTheDocument();
  });
});

describe("BoardSettings bare repos", () => {
  const owned = () => board({ repos: ["app"], automation_token_set: true, automation_account_id: "owner-fixture" });
  beforeEach(() => {
    vi.mocked(api).mockClear();
    catalog = { state: "known", engines: [] };
    me = { is_admin: true, is_human: true, account_id: "owner-fixture" };
    targets = { can_choose: true, default_runtime: "cluster", unresolved_repos: ["app"], targets: [
      { runtime: "cluster", label: "Cluster", unavailable: "app is a desktop folder name" },
      { runtime: "docker", daemon_id: "desktop-a", desktop_name: "Workstation", label: "Workstation · Local sandbox" },
      { runtime: "daemon", daemon_id: "desktop-a", desktop_name: "Workstation", label: "Workstation · This machine" },
      { runtime: "docker", daemon_id: "desktop-b", desktop_name: "Laptop", label: "Laptop · Local sandbox" },
    ] };
  });

  it("looks a bare repo up on the desktop the person picks, shows the owner/name, and saves it once confirmed", async () => {
    remote = { repo: "app", daemon_id: "desktop-b", desktop_name: "Laptop", has_folder: true, full_name: "example-org/app" };
    const onChanged = open(owned());
    const pick = await screen.findByRole("combobox", { name: "Desktop for app" });
    // Each desktop once; nothing is asked until the person chooses one.
    expect(screen.getAllByRole("option", { name: "Workstation" })).toHaveLength(1);
    // Desktops have no owner: the copy asks for the desktop the folder lives on, not "yours".
    expect(screen.getByRole("option", { name: "Pick the desktop this folder lives on…" })).toBeInTheDocument();
    expect(screen.getByText(/pick the desktop this folder lives on and look up its remote/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Look up app" })).toBeDisabled();
    expect(screen.getByRole("option", { name: /Cluster.*desktop folder name/ })).toBeDisabled();
    fireEvent.change(pick, { target: { value: "desktop-b" } });
    fireEvent.click(screen.getByRole("button", { name: "Look up app" }));
    expect(await screen.findByText("app → example-org/app")).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/boards/b1/repo-remote?repo=app&daemon_id=desktop-b");
    expect(api).not.toHaveBeenCalledWith("/api/boards/b1/repos/resolve", expect.anything());
    fireEvent.click(screen.getByRole("button", { name: "Save example-org/app" }));
    await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards/b1/repos/resolve", { method: "POST", json: { repo: "app", daemon_id: "desktop-b", full_name: "example-org/app" } }));
    await waitFor(() => expect(onChanged).toHaveBeenCalled());
  });

  it("says plainly that a folder with no remote runs on a desktop, and offers nothing to save", async () => {
    remote = { repo: "app", daemon_id: "desktop-a", desktop_name: "Workstation", has_folder: true, problem: "this folder has no remote, so the cluster can't clone it; run it on a desktop" };
    open(board({ repos: ["app"], automation_token_set: true, automation_account_id: "owner-fixture", run_runtime: "docker", run_daemon_id: "desktop-a" }));
    // The board's own desktop is the preset choice.
    const pick = await screen.findByRole("combobox", { name: "Desktop for app" });
    expect((pick as HTMLSelectElement).value).toBe("desktop-a");
    fireEvent.click(screen.getByRole("button", { name: "Look up app" }));
    expect(await screen.findByText("this folder has no remote, so the cluster can't clone it; run it on a desktop")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Save .*\// })).toBeNull();
  });

  it("shows no prompt when every repo is owner/name", async () => {
    targets = { ...(targets as object), unresolved_repos: [] };
    open(owned());
    await screen.findByRole("combobox", { name: "Where this board runs" });
    expect(screen.queryByText("Repos for the cluster")).toBeNull();
  });
});
