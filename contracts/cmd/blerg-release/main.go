// blerg-release is the release script's fail-closed GitHub publication gate.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/releasegate"
)

func main() {
	var r releasegate.Request
	var candidate, evidence string
	flag.StringVar(&candidate, "candidate", "", "accepted RC tag")
	flag.StringVar(&evidence, "evidence", "", "private upgrade acceptance JSON")
	flag.StringVar(&r.Tag, "tag", "", "release tag")
	flag.StringVar(&r.Notes, "notes", "", "Markdown release notes")
	flag.StringVar(&r.SHA, "sha", "", "exact public commit")
	flag.StringVar(&r.Parent, "parent", "", "expected public main")
	flag.StringVar(&r.Remote, "remote", "public", "public git remote")
	flag.StringVar(&r.Repository, "repo", "", "GitHub owner/repository")
	flag.Parse()
	fail := func(err error) { fmt.Fprintln(os.Stderr, "release-public:", err); os.Exit(1) }
	if _, err := releasegate.ParseVersion(r.Tag); err != nil {
		fail(err)
	}
	raw, err := os.ReadFile(r.Notes)
	if err != nil {
		fail(fmt.Errorf("cannot read release notes"))
	}
	if err := releasegate.ValidateNotes(raw); err != nil {
		fail(err)
	}
	if r.SHA == "" {
		return
	} // validation only: no remote or API side effects
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	remote, err := releasegate.Exec(ctx, "git", "remote", "get-url", "--push", r.Remote)
	if err != nil {
		fail(err)
	}
	url := strings.TrimSpace(string(remote))
	if r.Repository == "" || (url != "https://github.com/"+r.Repository+".git" && url != "git@github.com:"+r.Repository+".git") {
		fail(fmt.Errorf("public remote must match configured GitHub repository"))
	}
	if !strings.Contains(r.Tag, "-rc.") {
		base, e := releasegate.ParseVersion(candidate)
		if e != nil || !strings.HasPrefix(candidate, r.Tag+"-rc.") || "v"+base != r.Tag {
			fail(fmt.Errorf("stable publication requires a matching accepted candidate"))
		}
		// #nosec G304 -- The operator explicitly selects this local evidence file.
		data, e := os.ReadFile(evidence)
		if e != nil {
			fail(fmt.Errorf("cannot read acceptance evidence"))
		}
		var acceptance struct {
			Accepted bool   `json:"owner_accepted"`
			Tree     string `json:"candidate_tree"`
			Linux    string `json:"linux_desktop"`
			Mac      string `json:"macos_desktop"`
			Cluster  string `json:"kubernetes"`
		}
		if json.Unmarshal(data, &acceptance) != nil || !acceptance.Accepted || acceptance.Tree == "" || strings.TrimSpace(acceptance.Linux) == "" || strings.TrimSpace(acceptance.Mac) == "" || strings.TrimSpace(acceptance.Cluster) == "" {
			fail(fmt.Errorf("evidence must record owner acceptance and Linux, macOS, Kubernetes upgrade and restore drills"))
		}
		ref := "refs/blerg-candidate/" + candidate
		if _, e = releasegate.Exec(ctx, "git", "fetch", "--no-tags", r.Remote, "refs/tags/"+candidate+":"+ref); e != nil {
			fail(e)
		}
		tree, e := releasegate.Exec(ctx, "git", "rev-parse", ref+"^{tree}")
		if e != nil {
			fail(e)
		}
		target, e := releasegate.Exec(ctx, "git", "rev-parse", r.SHA+"^{tree}")
		if e != nil {
			fail(e)
		}
		if strings.TrimSpace(string(tree)) != acceptance.Tree || string(tree) != string(target) {
			fail(fmt.Errorf("stable tree differs from accepted RC; cut and test another candidate"))
		}
	}
	if err := (releasegate.Gate{Run: releasegate.Exec}).Publish(ctx, r); err != nil {
		fail(err)
	}
}
