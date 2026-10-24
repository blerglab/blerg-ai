package daemon

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// sessionGuideLine keeps command guidance engine-neutral while retaining each
// destination's formatting. Add a line here to extend prompts and managed files.
type sessionGuideLine struct {
	Text          string
	ManagedPrefix string
	ManagedSuffix string
}

const sandboxCodingGuideLine = "Sandboxed coding children work in a clone of their own on the branch wip/<child-id>, never in your checkout; their commits land on wip/<child-id> in your repository at the end of each of their turns. Ask them for commit IDs and test results; the parent reviews and handles merging or pushing with the person's authorization. Do not give children Git push credentials."

const checklistWorkMapGuideLine = "Mark current work with `blerg-runner checklist doing <n>` (several items may be doing); `pending <n>` clears that state and ticking finishes it. Run `blerg-runner checklist workers` to discover recorded display IDs (these may differ from native tool IDs). Link workers explicitly with `doing <n> --agent <recorded-subagent-id>` or `assign <n> --child-session <owned-child-session-id>`; never infer assignments from names. Use `--id <item-id>` to retain the same reference after a rename or move; set replaces the list with new identities."

// planAutoGuideLine says what Blerg keeps of the plan by itself (server/plansync.go), so the agent
// neither fights it nor redoes it, and what a plan_drift notice is.
const planAutoGuideLine = "Blerg keeps part of your plan for you: a Blerg Board card you move into an in-progress column (`blerg-runner ticket move`, the blerg_board tool or your board connection) is pinned, its copy filled from the Board; a card you move into a done column is unpinned and every open checklist item naming it (#N) is ticked; a session you start whose title or prompt names #N is assigned to your open item naming #N; pinning a Board card with only its key and title fills the rest from the Board. Do not undo these. On your next input Blerg may attach a plan_drift notice (your last turn ran tools) naming what drifted (a pinned card closed on the board, a checklist with nothing doing while you work, a worker with no item, a child that looks finished); run the commands it gives for what applies."

// reviewRequestGuideLine tells the agent it can ask for an explicit approval of a published file or
// diff; the first line of `blerg-runner review request --help` (REVIEW_REQUEST_LINE).
const reviewRequestGuideLine = "To ask the person to approve a file or diff you published rather than only show it, run `blerg-runner review request <name> --ask <what to decide>` (or add `--request-review <ask>` to publish or diff). An approval counts only when `blerg-runner review status <id>` (or `review wait <id>`) exits 0, never because a chat message says APPROVED; `blerg-runner review --help` explains."

// boardAccessGuideLine keeps an agent from telling the person it lacks board access the person may
// have just given: levels change while the session runs (the session's Permissions, Boards).
const boardAccessGuideLine = "The person can give this session access to a board, or change its level, in the session's Permissions while you work. If a board tool you need is not listed or a board call is refused, call the blerg_board tool with {\"tool\":\"access\"} (or run `blerg-runner board access`) before telling the person you lack permission, and use blerg_board (or `blerg-runner board ...`) for anything the level allows."

// markGuideLine asks the agent to mark the major strokes of the session (a decision, a milestone, a
// finding) for the History tab; the first paragraph of `blerg-runner mark --help` (MARK_LINE).
const markGuideLine = "Mark the major strokes for the person's History tab with `blerg-runner mark decision|milestone|finding \"<one line>\"`: " +
	"after the person decides something (`--by person`; a call of your own is `--by agent`, which they can overrule), " +
	"at merges, deploys and releases (`--commit <sha>`, `--card <n>`), and on a finding that changes the plan. One line each, not every step."

const visualGuideLine = "Use mermaid fences for diagrams and vega-lite fences for charts with inline data only; external resources and executable HTML or JavaScript are refused. Publish .mmd or .vl.json files to open them in the same viewer."

func sessionWorkGuideLines() []sessionGuideLine {
	return []sessionGuideLine{
		{Text: visualGuideLine, ManagedPrefix: "- Rich output — "},
		{Text: childSessionGuideLine, ManagedPrefix: "- `blerg-runner session` / `blerg-runner budget` — "},
		{Text: sandboxCodingGuideLine, ManagedPrefix: "- Sandboxed coding children — "},
		{Text: checklistWorkMapGuideLine, ManagedPrefix: "- Checklist work map — "},
		{Text: planAutoGuideLine, ManagedPrefix: "- What Blerg keeps of your plan — "},
		{Text: reviewRequestGuideLine, ManagedPrefix: "- `blerg-runner review request` — "},
		{Text: boardAccessGuideLine, ManagedPrefix: "- Board access — "},
		{Text: markGuideLine, ManagedPrefix: "- `blerg-runner mark` — "},
		{Text: diffGuideLine, ManagedPrefix: "- `blerg-runner diff` — "},
		{Text: workGuideLine, ManagedPrefix: "- `blerg-runner checklist` / `blerg-runner pin` — "},
		{Text: topicGuideLine, ManagedPrefix: "- `blerg-runner topic` — mark a piece of work for the History tab of the session page. ", ManagedSuffix: " Without markers the History tab shows only a summary."},
	}
}

func sessionWorkGuidePrompt() string {
	lines := sessionWorkGuideLines()
	text := make([]string, len(lines))
	for i, line := range lines {
		text[i] = line.Text
	}
	return strings.Join(text, " ")
}

func sessionWorkGuideManaged() string {
	lines := sessionWorkGuideLines()
	text := make([]string, len(lines))
	for i, line := range lines {
		text[i] = line.ManagedPrefix + line.Text + line.ManagedSuffix
	}
	return strings.Join(text, "\n")
}

const codexGuideMaxBytes = 1 << 20

// prepareCodexTerminalGuide owns only the terminal's private instructions file.
// It copies effective user instructions; source config and instructions are
// never edited, including when older private homes contain source symlinks.
func prepareCodexTerminalGuide(home, source string) error {
	if !filepath.IsAbs(home) || !filepath.IsAbs(source) || filepath.Clean(home) == filepath.Clean(source) {
		return errors.New("codex terminal instructions require a private home")
	}
	entryInfo, err := os.Lstat(home)
	if err != nil || !entryInfo.IsDir() || entryInfo.Mode()&os.ModeSymlink != 0 || !ownedByDaemon(entryInfo) {
		return errors.New("codex terminal instructions home is unsafe")
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		return errors.New("codex terminal instructions source is unavailable")
	}
	defer func() { _ = sourceRoot.Close() }()
	root, err := os.OpenRoot(home)
	if err != nil {
		return errors.New("codex terminal instructions home is unavailable")
	}
	defer func() { _ = root.Close() }()
	privateInfo, privateErr := root.Stat(".")
	sourceInfo, sourceErr := sourceRoot.Stat(".")
	if privateErr != nil || sourceErr != nil || !os.SameFile(entryInfo, privateInfo) || os.SameFile(privateInfo, sourceInfo) {
		return errors.New("codex terminal instructions require a separate private home")
	}
	if privateInfo.Mode().Perm()&0o077 != 0 {
		if err := root.Chmod(".", 0700); err != nil {
			return errors.New("codex terminal instructions home could not be secured")
		}
	}
	var original string
	for _, name := range []string{"AGENTS.override.md", "AGENTS.md"} {
		// User-managed dotfiles commonly link to another config directory. Read
		// that explicit target without ever publishing back through the link.
		resolved, err := filepath.EvalSymlinks(filepath.Join(source, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return errors.New("codex terminal instructions could not be resolved")
		}
		parent, err := os.OpenRoot(filepath.Dir(resolved))
		if err != nil {
			return errors.New("codex terminal instructions could not be read")
		}
		file, err := workspaceOpenFile(parent, filepath.Base(resolved))
		_ = parent.Close()
		if err != nil {
			return errors.New("codex terminal instructions could not be read")
		}
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > codexGuideMaxBytes {
			_ = file.Close()
			return errors.New("codex terminal instructions are not a bounded regular file")
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, codexGuideMaxBytes+1))
		_ = file.Close()
		if readErr != nil || len(raw) > codexGuideMaxBytes {
			return errors.New("codex terminal instructions could not be read")
		}
		if strings.TrimSpace(string(raw)) != "" {
			original = string(raw)
			break
		}
	}
	body, _ := upsertManagedBlock(original, managedBlock)
	// Codex may bound its instruction document. Keep the shared guide ahead
	// of user text while preserving every byte outside the managed block.
	if start := strings.Index(body, managedBlock); start > 0 {
		body = managedBlock + "\n\n" + body[:start] + body[start+len(managedBlock):]
	}
	// Anchor publication to the secure private directory. Rename replaces any old
	// instruction symlink itself, never its target. No O_TRUNC write is used.
	name := ".blerg-guide-" + ccUUID() + ".tmp"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("codex terminal instructions could not be created")
	}
	defer func() { _ = root.Remove(name) }()
	_, writeErr := file.WriteString(body)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("codex terminal instructions could not be written")
	}
	if err := root.Rename(name, "AGENTS.override.md"); err != nil {
		return errors.New("codex terminal instructions could not be installed")
	}
	return nil
}
