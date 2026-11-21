// Package releasegate enforces publication ordering. Command failures deliberately
// omit subprocess output: authenticated tools may put credentials in diagnostics.
package releasegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$`)

func ParseVersion(tag string) (string, error) {
	if !versionPattern.MatchString(tag) {
		return "", errors.New("expected canonical vX.Y.Z or vX.Y.Z-rc.N")
	}
	return strings.Split(strings.TrimPrefix(tag, "v"), "-rc.")[0], nil
}

func ValidateNotes(raw []byte) error {
	in := false
	level := 0
	fenced := false
	var section []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if !fenced && strings.HasPrefix(line, "#") {
			n := len(line) - len(strings.TrimLeft(line, "#"))
			if strings.TrimSpace(strings.TrimLeft(line, "#")) == "How to upgrade" {
				in = true
				level = n
				continue
			}
			if in && n <= level && strings.HasPrefix(line, strings.Repeat("#", n)+" ") {
				break
			}
		}
		if in {
			section = append(section, line)
		}
	}
	s := strings.ToLower(strings.Join(section, "\n"))
	if !strings.Contains(s, "desktop") || !strings.Contains(s, "kubernetes") {
		return errors.New("release notes need a nonempty How to upgrade section with desktop and Kubernetes instructions")
	}
	return nil
}

type Command func(context.Context, string, ...string) ([]byte, error)
type Gate struct{ Run Command }
type Request struct{ Remote, Repository, SHA, Parent, Tag, Notes string }

func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- Internal callers choose git or gh and pass arguments without a shell.
	b, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s command failed (output withheld)", name)
	}
	return b, nil
}
func (g Gate) api(ctx context.Context, path string, out any) error {
	b, err := g.Run(ctx, "gh", "api", "repos/"+path)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, out); err != nil {
		return errors.New("invalid GitHub response")
	}
	return nil
}

type workflowRun struct {
	ID         int64  `json:"id"`
	Number     int64  `json:"run_number"`
	Attempt    int    `json:"run_attempt"`
	SHA        string `json:"head_sha"`
	Branch     string `json:"head_branch"`
	Event      string `json:"event"`
	Path       string `json:"path"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func (g Gate) green(ctx context.Context, r Request) error {
	latest := map[string]workflowRun{}
	for page := 1; ; page++ {
		var data struct {
			Runs []workflowRun `json:"workflow_runs"`
		}
		path := fmt.Sprintf("%s/actions/runs?head_sha=%s&branch=ci/%s&event=push&per_page=100&page=%d", r.Repository, r.SHA, r.Tag, page)
		if err := g.api(ctx, path, &data); err != nil {
			return err
		}
		for _, run := range data.Runs {
			if run.SHA != r.SHA || run.Branch != "ci/"+r.Tag || run.Event != "push" {
				continue
			}
			if run.Path != ".github/workflows/ci.yml" && run.Path != ".github/workflows/workspace-explorer.yml" {
				continue
			}
			old := latest[run.Path]
			if run.Number > old.Number || (run.Number == old.Number && run.Attempt > old.Attempt) {
				latest[run.Path] = run
			}
		}
		if len(data.Runs) < 100 {
			break
		}
		if page >= 100 {
			return errors.New("too many workflow pages")
		}
	}
	for _, path := range []string{".github/workflows/ci.yml", ".github/workflows/workspace-explorer.yml"} {
		run, ok := latest[path]
		if !ok || run.Status != "completed" {
			return errPending
		}
		if run.Conclusion != "success" {
			return fmt.Errorf("%s run %d attempt %d is not green", path, run.ID, run.Attempt)
		}
		linux, mac := false, false
		for page := 1; ; page++ {
			var data struct {
				Jobs []struct{ Name, Status, Conclusion string } `json:"jobs"`
			}
			if err := g.api(ctx, fmt.Sprintf("%s/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", r.Repository, run.ID, run.Attempt, page), &data); err != nil {
				return err
			}
			for _, job := range data.Jobs {
				if job.Status != "completed" || job.Conclusion != "success" {
					return errors.New("workflow has an unsuccessful or skipped job")
				}
				linux = linux || job.Name == "descriptor-traversal (ubuntu-latest)"
				mac = mac || job.Name == "descriptor-traversal (macos-latest)"
			}
			if len(data.Jobs) < 100 {
				break
			}
			if page >= 100 {
				return errors.New("too many job pages")
			}
		}
		if strings.HasSuffix(path, "workspace-explorer.yml") && (!linux || !mac) {
			return errors.New("missing successful Linux and macOS security jobs")
		}
	}
	return nil
}

var errPending = errors.New("waiting for both exact-SHA workflows")

func (g Gate) unchanged(ctx context.Context, r Request) error {
	b, err := g.Run(ctx, "git", "ls-remote", r.Remote, "refs/heads/main", "refs/tags/"+r.Tag)
	if err != nil {
		return err
	}
	parent := ""
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		if f[1] == "refs/tags/"+r.Tag {
			return errors.New("tag already exists; never move a published tag")
		}
		if f[1] == "refs/heads/main" {
			parent = f[0]
		}
	}
	if parent != r.Parent {
		return errors.New("public main changed; rebuild and rerun CI")
	}
	return nil
}

func (g Gate) Publish(ctx context.Context, r Request) error {
	if _, err := ParseVersion(r.Tag); err != nil {
		return err
	}
	if err := g.unchanged(ctx, r); err != nil {
		return err
	}
	if _, err := g.Run(ctx, "git", "push", r.Remote, r.SHA+":refs/heads/ci/"+r.Tag); err != nil {
		return err
	}
	for {
		err := g.green(ctx, r)
		if err == nil {
			break
		}
		if !errors.Is(err, errPending) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
	}
	// Check again immediately before refs become installable. A new failed rerun
	// must not be hidden by the earlier successful one.
	if err := g.green(ctx, r); err != nil {
		return err
	}
	if err := g.unchanged(ctx, r); err != nil {
		return err
	}
	push := []string{"push", "--atomic", "--force-with-lease=refs/heads/main:" + r.Parent, r.Remote}
	if !strings.Contains(r.Tag, "-rc.") {
		push = append(push, r.SHA+":refs/heads/main")
	}
	push = append(push, r.SHA+":refs/tags/"+r.Tag)
	if _, err := g.Run(ctx, "git", push...); err != nil {
		return err
	}
	args := []string{"release", "create", r.Tag, "--repo", r.Repository, "--verify-tag", "--title", r.Tag, "--notes-file", r.Notes}
	if strings.Contains(r.Tag, "-rc.") {
		args = append(args, "--prerelease")
	}
	if _, err := g.Run(ctx, "gh", args...); err != nil {
		return errors.New("tag published but release creation failed; keep the tag immutable and investigate before retrying")
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := g.api(ctx, r.Repository+"/releases/tags/"+r.Tag, &release); err != nil {
		return err
	}
	if release.Tag != r.Tag || release.Draft || release.Prerelease != strings.Contains(r.Tag, "-rc.") {
		return errors.New("published release metadata does not match request")
	}
	return nil
}
