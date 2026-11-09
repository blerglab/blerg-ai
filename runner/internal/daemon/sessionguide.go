package daemon

import (
	"strings"

	"github.com/blerglab/blerg-ai/runner/skills"
)

// sessionGuideLine keeps command guidance engine-neutral while retaining each
// destination's formatting. Add a line here to extend prompts and managed files.
type sessionGuideLine struct {
	Text          string
	ManagedPrefix string
	ManagedSuffix string
}

const sandboxCodingGuideLine = "Sandboxed coding children work in their own clone; host children of workspace sessions work in their own worktrees. Both use their recorded session branches and leave the parent checkout unchanged. Sandbox commits arrive in your repository at turn end; host worktree commits land there directly. Ask them for commit IDs and test results; the parent reviews and handles merging or pushing with the person's authorization. Do not give children Git push credentials."

const checklistWorkMapGuideLine = "Mark current work with `blerg-runner checklist doing <n>` (several items may be doing); `pending <n>` clears that state and ticking finishes it. Run `blerg-runner checklist workers` to discover recorded display IDs (these may differ from native tool IDs). Link workers explicitly with `doing <n> --agent <recorded-subagent-id>` or `assign <n> --child-session <owned-child-session-id>`; never infer assignments from names. Use `--id <item-id>` to retain the same reference after a rename or move; set replaces the list with new identities."

const usageNoticeGuideLine = "Blerg sends usage_limit notices to working top-level sessions for their engine, once per threshold per usage window, and catches up when a turn starts. At 80% prefer other engines for new children and reviews; at 90% start nothing new on this engine and let running work finish; at 95% finish the current step and save state. Administrators can adjust these thresholds; follow the guidance in the authenticated notice. A notice includes the engine, window, percentage, reset time, other engines’ headroom and your running children on that engine."

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

const ticketEstimateGuideLine = "Before filing or starting any ticket, estimate size S/M/L/XL, risk low/medium/high and complexity low/medium/high using first-class card fields (ticket create/update --size --risk --complexity). Record every review with blerg-runner mark review \"<summary>\" --card N --verdict approved|changes|rejected --by <engine> --target <sha> --round N; add --findings with one high, medium or low per finding, omit for none. Record failures with blerg-runner mark failure \"<summary>\" --card N --type test|gate|flake|review|session|engine|deploy|merge --severity high|medium|low --suite <name>; record a successful rerun with --rerun-passed, preserving the original failure. Server session failure alerts link automatically. These rules apply to every workspace, channel and worker."

const visualGuideLine = "Use mermaid fences for diagrams and vega-lite fences for charts with inline data only; external resources and executable HTML or JavaScript are refused. Charts may theme through config and use layers, facets, repeats and concats; data transforms are not available yet, and transform, calculate, expr, params and url are refused. Publish .mmd or .vl.json files to open them in the same viewer."

// Tool defaults in cluster pods come from the pod's own downward-API limits.
const podToolBudgetGuideLine = "In agent pods, size build and test parallelism from BLERG_TOOL_WORKERS, not host core counts. The image sets GOMAXPROCS, GOFLAGS=-p=<workers>, VITEST_MAX_WORKERS, UV_THREADPOOL_SIZE, NODE_OPTIONS heap limits, RAYON_NUM_THREADS, OMP_NUM_THREADS, MAKEFLAGS and CMAKE_BUILD_PARALLEL_LEVEL from the pod CPU/memory limits. Vitest reads VITEST_MAX_WORKERS; use Playwright --workers=$PLAYWRIGHT_WORKERS and explicit tool worker flags where needed. Run memory-heavy suites one at a time, keep these limits, and lower workers further if memory is tight. Workspace files and mid-turn commits on the retained volume survive pod loss; resume restores them even if that turn never pushed its session branch."

// keepWorkingGuideLine stops a session going idle on an offer: waiting for the person
// is for real decisions, and only once nothing safe remains.
const keepWorkingGuideLine = "While you wait for the person, keep doing the safe, reversible work that is already within what they asked for and does not depend on their answer. Start it rather than offering it. Stop only when everything left needs them. Never go past their request, make a decision that is theirs, or take a destructive, outward-facing or irreversible step to stay busy."

func sessionWorkGuideLines() []sessionGuideLine {
	return []sessionGuideLine{
		{Text: skills.AlwaysOn},
		{Text: keepWorkingGuideLine},
		{Text: "For Main or a channel, pick one emoji that suits your work with `blerg-runner title --emoji \"🔎\"`; the person can change it from the channel row menu.", ManagedPrefix: "- Channel emoji — "},
		{Text: visualGuideLine, ManagedPrefix: "- Rich output — "},
		{Text: childSessionGuideLine, ManagedPrefix: "- `blerg-runner session` / `blerg-runner budget` — "},
		{Text: sandboxCodingGuideLine, ManagedPrefix: "- Coding child workspaces — "},
		{Text: podToolBudgetGuideLine, ManagedPrefix: "- Agent pod tool budgets — "},
		{Text: checklistWorkMapGuideLine, ManagedPrefix: "- Checklist work map — "},
		{Text: usageNoticeGuideLine, ManagedPrefix: "- Usage-limit notices — "},
		{Text: planAutoGuideLine, ManagedPrefix: "- What Blerg keeps of your plan — "},
		{Text: reviewRequestGuideLine, ManagedPrefix: "- `blerg-runner review request` — "},
		{Text: boardAccessGuideLine, ManagedPrefix: "- Board access — "},
		{Text: ticketEstimateGuideLine, ManagedPrefix: "- Ticket estimates and reviews — "},
		{Text: ticketWorkflowGuide(), ManagedPrefix: "- Ticket workflow skill — "},
		{Text: skillCatalogEntry("test-timing"), ManagedPrefix: "- Test timing skill — "},
		{Text: "When acting as Main, apply blerg:coordinating; workers keep their assigned scope. " + skillCatalogEntry("coordinating"), ManagedPrefix: "- Coordinating workflow skill — "},
		{Text: skillCatalogEntry("test-driven-development"), ManagedPrefix: "- Blerg test-driven-development skill — "},
		{Text: skillCatalogEntry("planning"), ManagedPrefix: "- Blerg planning skill — "},
		{Text: skillCatalogEntry("ux-design"), ManagedPrefix: "- Blerg ux-design skill — "},
		{Text: skillCatalogEntry("visualizing"), ManagedPrefix: "- Blerg visualizing skill — "},
		{Text: markGuideLine, ManagedPrefix: "- `blerg-runner mark` — "},
		{Text: diffGuideLine, ManagedPrefix: "- `blerg-runner diff` — "},
		{Text: workGuideLine, ManagedPrefix: "- `blerg-runner checklist` / `blerg-runner pin` — "},
		{Text: topicGuideLine, ManagedPrefix: "- `blerg-runner topic` — mark a piece of work for the History tab of the session page. ", ManagedSuffix: " Without markers the History tab shows only a summary."},
	}
}

// Keep the shared guide on one line, as the CLI adapter's prompt argument has
// always been. The topic guide remains last; the skill is also in managed files.
func ticketWorkflowGuide() string {
	return skillCatalogEntry("managing-tickets")
}

func workflowSkillGuide(body string) string {
	if parts := strings.SplitN(body, "---", 3); len(parts) == 3 {
		body = parts[2]
	}
	return strings.Join(strings.Fields(body), " ")
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
	var text []string
	for _, line := range lines {
		if line.ManagedPrefix == "- Blerg planning skill — " || line.ManagedPrefix == "- Coordinating workflow skill — " || line.ManagedPrefix == "- Blerg test-driven-development skill — " || line.ManagedPrefix == "- Blerg ux-design skill — " || line.ManagedPrefix == "- Blerg visualizing skill — " {
			continue // Shared home instructions cannot express a session's policy.
		}
		text = append(text, line.ManagedPrefix+line.Text+line.ManagedSuffix)
	}
	return strings.Join(text, "\n")
}

// Catalog entries are also used to remove disabled skills from shared prompts.
func skillCatalogEntry(name string) string {
	for _, e := range skills.Inventory(skills.Resolve(true)) {
		if e.Name == name {
			return "blerg:" + e.Name + ": " + e.Description + ". Load its SKILL.md before applicable work; subagents must do the same."
		}
	}
	return ""
}
