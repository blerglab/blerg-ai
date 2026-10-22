package api

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
)

// Bare board repos (a desktop folder name such as "app", not owner/name).
//
// A desktop finds such a repo by its folder. The cluster clones, so it needs owner/name, and the
// runner resolves a bare name for a cluster start only from the install's default organization or
// git base, or from a desktop the start names. A board's cluster runs name no desktop, so a bare
// repo is resolved once, here, with the person present: they pick their desktop, the board shows
// "app → owner/app" from that desktop's remote, and saves owner/name when they confirm.
//
// Desktops have no owner in the runner, so the board cannot tell whose desktop is whose: the lookup
// answers for whichever desktop the person picks ("the desktop this folder lives on"), and only
// that desktop's remote is consulted, never another's. Resolving is therefore kept to a signed-in
// board admin who also owns the board's automation token (the account cluster runs start as, and
// the one person the board settings show the prompt to).

// msgNoRemote: the folder on the chosen desktop has no remote the cluster could clone.
const msgNoRemote = "this folder has no remote, so the cluster can't clone it; run it on a desktop"

// validFullName bounds an owner/name a desktop reports before the board stores it.
var validFullName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9_-][A-Za-z0-9_.-]{0,99}$`)

// bareRepo reports whether repo is a folder name rather than owner/name.
func bareRepo(repo string) bool { return repo != "" && !strings.Contains(repo, "/") }

// msgBareRepoCluster says why bare repos cannot run on the cluster yet.
func msgBareRepoCluster(repos ...string) string {
	if len(repos) == 1 {
		return fmt.Sprintf("%s is a desktop folder name, not owner/name, so the cluster can't clone it: resolve it under Repos in board settings, or run it on a desktop", repos[0])
	}
	return fmt.Sprintf("%s are desktop folder names, not owner/name, so the cluster can't clone them: resolve them under Repos in board settings, or run them on a desktop", strings.Join(repos, ", "))
}

// bareReposBlockCluster reports whether a bare repo on this board would be refused by the
// cluster: the runner offers a cluster that does not resolve bare names itself (no default
// organization or git base), and neither the board nor the board's runner sends a clone URL.
func (a *API) bareReposBlockCluster(board db.Board, options runner.LaunchOptions) bool {
	if board.GitBase != "" {
		return false
	}
	if d, ok := a.runner.Driver.(interface{ GitBase() string }); ok && d.GitBase() != "" {
		return false
	}
	for _, t := range options.Targets {
		if t.DaemonID == "" && slices.Contains(t.Runtimes, "cluster") {
			return !t.ResolvesBareRepos
		}
	}
	return false
}

// unresolvedRepos are the board's bare repos that the cluster would refuse.
func (a *API) unresolvedRepos(board db.Board, options runner.LaunchOptions) []string {
	out := []string{}
	if !a.bareReposBlockCluster(board, options) {
		return out
	}
	for _, repo := range board.Repos {
		if bareRepo(repo) {
			out = append(out, repo)
		}
	}
	return out
}

// clusterReposProblem is the refusal for running repos on the cluster from this board, naming
// every bare one the cluster would refuse, or nil.
func (a *API) clusterReposProblem(ctx context.Context, board db.Board, repos []string) error {
	var bare []string
	for _, repo := range repos {
		if bareRepo(repo) && boardGitURL(board, repo) == "" {
			bare = append(bare, repo)
		}
	}
	if len(bare) == 0 {
		return nil
	}
	options, err := a.discoverRunTargets(ctx, board.ID, "")
	if err != nil {
		return &automationError{"could not check where this board runs; retry after the runner reconnects"}
	}
	if a.bareReposBlockCluster(board, options) {
		return &automationError{msgBareRepoCluster(bare...)}
	}
	return nil
}

// repoRemote is what the person's chosen desktop says about one bare board repo.
type repoRemote struct {
	Repo        string `json:"repo"`
	DaemonID    string `json:"daemon_id"`
	DesktopName string `json:"desktop_name"`
	HasFolder   bool   `json:"has_folder"`
	// FullName is the owner/name the board would save; empty when Problem says why not.
	FullName string `json:"full_name,omitempty"`
	Provider string `json:"provider,omitempty"`
	Problem  string `json:"problem,omitempty"`
}

// lookupRepoRemote asks the runner about repo on the one desktop daemonID, and reads only that
// desktop's answer.
func (a *API) lookupRepoRemote(ctx context.Context, boardID, repo, daemonID string) (repoRemote, error) {
	out := repoRemote{Repo: repo, DaemonID: daemonID}
	d, ok := a.runner.Driver.(runner.RepoOriginDiscoverer)
	if !ok {
		return out, &automationError{"this runner cannot report a desktop's git remotes"}
	}
	token, err := a.startIdentity(ctx, boardID)
	if err != nil {
		return out, err
	}
	options, err := d.DiscoverRepoOrigin(ctx, token, repo, daemonID)
	if err != nil {
		return out, &automationError{"could not ask the runner about that desktop; retry after it reconnects"}
	}
	var desktop *runner.LaunchTarget
	for i := range options.Targets {
		if options.Targets[i].DaemonID == daemonID {
			desktop = &options.Targets[i]
			break
		}
	}
	if desktop == nil {
		return out, &automationError{"that desktop is not connected; open Blerg on it, then look again"}
	}
	out.DesktopName = desktop.Name
	out.HasFolder = desktop.HasRepo != nil && *desktop.HasRepo
	origin := desktop.RepoOrigin
	switch {
	case !out.HasFolder:
		out.Problem = fmt.Sprintf("%s has no folder named %s; pick the desktop that has it", desktop.Name, repo)
	case origin == nil:
		out.Problem = msgNoRemote
	case origin.Provider != "github":
		out.Problem = fmt.Sprintf("this folder's remote is on %s; a board's cluster runs clone GitHub repositories unless the board has a git base, so run it on a desktop", origin.Provider)
	case !validFullName.MatchString(origin.FullName):
		out.Problem = msgNoRemote
	default:
		out.FullName, out.Provider = origin.FullName, origin.Provider
	}
	return out, nil
}

// msgResolveRepoWho: who may look a bare repo up and resolve it.
const msgResolveRepoWho = "resolving a board's repo needs a signed-in board admin who owns the board's automation token"

// bareRepoRequest checks a repo-resolution request: a signed-in board admin who owns the board's
// automation token, a bare repo the board
// has, a well-formed desktop id and a runner. It answers the refusal itself and returns ok=false.
func (a *API) bareRepoRequest(w http.ResponseWriter, r *http.Request, p auth.Principal, repo, daemonID string) (db.Board, bool) {
	boardID := r.PathValue("id")
	if p.HumanAccountID() == "" || p.RequireAdmin(boardID) != nil {
		writeError(w, http.StatusForbidden, msgResolveRepoWho)
		return db.Board{}, false
	}
	board, err := db.GetBoard(r.Context(), a.Pool, boardID)
	if err != nil {
		writeDBError(w, err)
		return db.Board{}, false
	}
	if !runTargetChooser(p, board) {
		writeError(w, http.StatusForbidden, msgResolveRepoWho)
		return db.Board{}, false
	}
	if !bareRepo(repo) || !slices.Contains(board.Repos, repo) {
		writeError(w, http.StatusUnprocessableEntity, "repo must be one of this board's repos given as a folder name")
		return db.Board{}, false
	}
	if !validDaemonID.MatchString(daemonID) {
		writeError(w, http.StatusUnprocessableEntity, "daemon_id must name the desktop to look on")
		return db.Board{}, false
	}
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return db.Board{}, false
	}
	return board, true
}

// handleBoardRepoRemote answers GET /api/boards/{id}/repo-remote?repo=&daemon_id=: what the
// chosen desktop's folder's remote is, for the person to confirm. Nothing is saved.
func (a *API) handleBoardRepoRemote(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	repo, daemonID := r.URL.Query().Get("repo"), r.URL.Query().Get("daemon_id")
	board, ok := a.bareRepoRequest(w, r, p, repo, daemonID)
	if !ok {
		return
	}
	out, err := a.lookupRepoRemote(r.Context(), board.ID, repo, daemonID)
	if err != nil {
		writeStartError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, out)
}

// handleResolveBoardRepo answers POST /api/boards/{id}/repos/resolve {repo, daemon_id,
// full_name}: the person confirmed full_name for repo; the board asks the same desktop again and
// saves owner/name, on the board and its cards, only when the desktop still says so.
func (a *API) handleResolveBoardRepo(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	body, err := decode[struct {
		Repo     string `json:"repo"`
		DaemonID string `json:"daemon_id"`
		FullName string `json:"full_name"`
	}](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	board, ok := a.bareRepoRequest(w, r, p, body.Repo, body.DaemonID)
	if !ok {
		return
	}
	found, err := a.lookupRepoRemote(r.Context(), board.ID, body.Repo, body.DaemonID)
	if err != nil {
		writeStartError(w, err)
		return
	}
	if found.Problem != "" {
		writeError(w, http.StatusUnprocessableEntity, found.Problem)
		return
	}
	if !strings.EqualFold(found.FullName, body.FullName) {
		writeError(w, http.StatusConflict, fmt.Sprintf("%s now reports %s for %s; look again", found.DesktopName, found.FullName, body.Repo))
		return
	}
	b, err := db.ResolveBoardRepo(r.Context(), a.Pool, board.ID, body.Repo, found.FullName)
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(board.ID, "board_changed")
	writeJSON(w, http.StatusOK, b)
}
