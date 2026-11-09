package releasegate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestVersionAndUpgradeNotes(t *testing.T) {
	for _, v := range []string{"v0.6.0", "v0.6.0-rc.1"} {
		if _, err := ParseVersion(v); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"v01.6.0", "v0.6.0-rc.0", "v0.6.0-rc.01", "main", "--help"} {
		if _, err := ParseVersion(v); err == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, notes := range []string{"", "## How to upgrade\n", "## How to upgrade\nDesktop only\n## Other\nKubernetes"} {
		if ValidateNotes([]byte(notes)) == nil {
			t.Fatalf("accepted %q", notes)
		}
	}
	if err := ValidateNotes([]byte("## How to upgrade\nDesktop: backup first.\nKubernetes: backup first.\n")); err != nil {
		t.Fatal(err)
	}
}

func TestExactSHAGreenAndOrdering(t *testing.T) {
	for _, bad := range []string{"", "sha", "branch", "event", "red", "missing", "platform", "later-red", "api"} {
		t.Run(bad, func(t *testing.T) {
			var journal []string
			g := Gate{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
				s := strings.Join(args, " ")
				journal = append(journal, name+" "+s)
				if name == "git" {
					if strings.HasPrefix(s, "ls-remote") {
						return nil, nil
					}
					return nil, nil
				}
				if strings.Contains(s, "/actions/runs?") {
					if bad == "api" {
						return nil, fmt.Errorf("private error")
					}
					runs := []map[string]any{}
					for i, w := range []string{"ci.yml", "workspace-explorer.yml"} {
						r := map[string]any{"id": i + 1, "run_number": 1, "run_attempt": 1, "head_sha": "abc", "head_branch": "ci/v0.6.0", "event": "push", "path": ".github/workflows/" + w, "status": "completed", "conclusion": "success"}
						if bad == "sha" {
							r["head_sha"] = "other"
						}
						if bad == "branch" {
							r["head_branch"] = "main"
						}
						if bad == "event" {
							r["event"] = "pull_request"
						}
						if bad == "red" {
							r["conclusion"] = "failure"
						}
						if bad != "missing" || i == 0 {
							runs = append(runs, r)
						}
						if bad == "later-red" {
							red := map[string]any{}
							for k, v := range r {
								red[k] = v
							}
							red["id"] = i + 10
							red["run_number"] = 2
							red["conclusion"] = "failure"
							runs = append(runs, red)
						}
					}
					return json.Marshal(map[string]any{"workflow_runs": runs})
				}
				if strings.Contains(s, "/jobs?") {
					jobs := []map[string]string{{"name": "descriptor-traversal (ubuntu-latest)", "status": "completed", "conclusion": "success"}, {"name": "descriptor-traversal (macos-latest)", "status": "completed", "conclusion": "success"}}
					if bad == "platform" {
						jobs = jobs[:1]
					}
					return json.Marshal(map[string]any{"jobs": jobs})
				}
				if strings.Contains(s, "releases/tags/") {
					return []byte(`{"tag_name":"v0.6.0","target_commitish":"abc","draft":false,"prerelease":false}`), nil
				}
				return []byte(`{}`), nil
			}}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			err := g.Publish(ctx, Request{Remote: "public", Repository: "example-org/example-repo", SHA: "abc", Tag: "v0.6.0", Notes: "notes.md"})
			if bad != "" {
				if err == nil {
					t.Fatal("gate accepted invalid evidence")
				}
				for _, s := range journal {
					if strings.Contains(s, "--atomic") || strings.Contains(s, "release create") {
						t.Fatalf("published after failed gate: %v", journal)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			all := strings.Join(journal, "\n")
			ci := strings.Index(all, "abc:refs/heads/ci/v0.6.0")
			check := strings.Index(all, "/actions/runs?")
			atomic := strings.Index(all, "--atomic")
			release := strings.Index(all, "release create")
			if ci < 0 || check < ci || atomic < check || release < atomic {
				t.Fatal(all)
			}
		})
	}
}

// Exercise actual git pushes against a bare remote; the API is the only fake.
func TestBareRemoteNeverPublishesOnRed(t *testing.T) {
	for _, green := range []bool{false, true} {
		t.Run(fmt.Sprint(green), func(t *testing.T) {
			dir := t.TempDir()
			remote := dir + "/public.git"
			work := dir + "/work"
			git := func(args ...string) string {
				t.Helper()
				b, e := Exec(t.Context(), "git", args...)
				if e != nil {
					t.Fatal(e)
				}
				return strings.TrimSpace(string(b))
			}
			git("init", "--bare", remote)
			git("init", work)
			git("-C", work, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "release")
			sha := git("-C", work, "rev-parse", "HEAD")
			g := Gate{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "git" {
					return Exec(ctx, name, append([]string{"-C", work}, args...)...)
				}
				s := strings.Join(args, " ")
				if strings.Contains(s, "/actions/runs?") {
					conclusion := "failure"
					if green {
						conclusion = "success"
					}
					return json.Marshal(map[string]any{"workflow_runs": []map[string]any{
						{"id": 1, "run_number": 1, "run_attempt": 1, "head_sha": sha, "head_branch": "ci/v0.6.0", "event": "push", "path": ".github/workflows/ci.yml", "status": "completed", "conclusion": conclusion},
						{"id": 2, "run_number": 1, "run_attempt": 1, "head_sha": sha, "head_branch": "ci/v0.6.0", "event": "push", "path": ".github/workflows/workspace-explorer.yml", "status": "completed", "conclusion": conclusion},
					}})
				}
				if strings.Contains(s, "/jobs?") {
					return []byte(`{"jobs":[{"name":"descriptor-traversal (ubuntu-latest)","status":"completed","conclusion":"success"},{"name":"descriptor-traversal (macos-latest)","status":"completed","conclusion":"success"}]}`), nil
				}
				if strings.Contains(s, "releases/tags/") {
					return []byte(`{"tag_name":"v0.6.0"}`), nil
				}
				return nil, nil
			}}
			err := g.Publish(t.Context(), Request{Remote: remote, Repository: "example-org/example-repo", SHA: sha, Tag: "v0.6.0"})
			refs := git("--git-dir="+remote, "show-ref")
			if !strings.Contains(refs, "refs/heads/ci/v0.6.0") {
				t.Fatal("missing CI push")
			}
			if green {
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(refs, "refs/tags/v0.6.0") {
					t.Fatal("tag missing")
				}
			} else if err == nil || strings.Contains(refs, "refs/tags/") || strings.Contains(refs, "refs/heads/main") {
				t.Fatalf("red mutated public refs: %s", refs)
			}
		})
	}
}

func TestUpgradeNotesAllowSubheadingsAndShellComments(t *testing.T) {
	if err := ValidateNotes([]byte("## How to upgrade\n### Desktop\n```sh\n# back up first\n```\nRun installer.\n### Kubernetes\nDeploy after backup.\n## Other\n")); err != nil {
		t.Fatal(err)
	}
}
func TestCandidateDoesNotAdvanceMain(t *testing.T) {
	g := Gate{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		s := strings.Join(args, " ")
		if name == "git" && strings.Contains(s, "--atomic") && strings.Contains(s, ":refs/heads/main") {
			t.Fatal("RC advanced main")
		}
		if strings.Contains(s, "actions/runs?") {
			return []byte(`{"workflow_runs":[{"id":1,"run_number":1,"run_attempt":1,"head_sha":"abc","head_branch":"ci/v0.6.0-rc.1","event":"push","path":".github/workflows/ci.yml","status":"completed","conclusion":"success"},{"id":2,"run_number":1,"run_attempt":1,"head_sha":"abc","head_branch":"ci/v0.6.0-rc.1","event":"push","path":".github/workflows/workspace-explorer.yml","status":"completed","conclusion":"success"}]}`), nil
		}
		if strings.Contains(s, "/jobs?") {
			return []byte(`{"jobs":[{"name":"descriptor-traversal (ubuntu-latest)","status":"completed","conclusion":"success"},{"name":"descriptor-traversal (macos-latest)","status":"completed","conclusion":"success"}]}`), nil
		}
		if strings.Contains(s, "releases/tags/") {
			return []byte(`{"tag_name":"v0.6.0-rc.1","prerelease":true}`), nil
		}
		return nil, nil
	}}
	if err := g.Publish(t.Context(), Request{SHA: "abc", Tag: "v0.6.0-rc.1"}); err != nil {
		t.Fatal(err)
	}
}
