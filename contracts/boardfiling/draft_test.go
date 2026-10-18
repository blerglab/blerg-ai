package boardfiling

import (
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestDigestBindsTargetAndEveryApprovedField(t *testing.T) {
	original := Draft{Repos: []string{"target/repo"}, Title: "Fixture title", Body: "Fixture reproduction", Type: "bug", Sensitive: true, Origin: Origin{SessionURL: "https://runner.example.test/sessions/fixture", Repo: "fixture/repo", Commit: "abcdef0", Engine: "codex", ComponentVersion: "0.4.0", ComponentCommit: "abcdef1"}}
	baseline := Digest("board-one", original)
	if decoded, err := hex.DecodeString(baseline); err != nil || len(decoded) != 32 {
		t.Fatal("digest must be SHA256 lowerhex")
	}
	changes := []func(*Draft){
		func(d *Draft) { d.Repos = []string{"other/repo"} },
		func(d *Draft) { d.Title += " changed" }, func(d *Draft) { d.Body += " changed" }, func(d *Draft) { d.Type = "task" }, func(d *Draft) { d.Sensitive = false },
		func(d *Draft) { d.Origin.SessionURL += "-other" }, func(d *Draft) { d.Origin.Repo += "-other" }, func(d *Draft) { d.Origin.Commit = "abcdef2" }, func(d *Draft) { d.Origin.Engine = "claude" }, func(d *Draft) { d.Origin.ComponentVersion = "0.5.0" }, func(d *Draft) { d.Origin.ComponentCommit = "abcdef3" },
	}
	for i, change := range changes {
		changed := original
		change(&changed)
		if Digest("board-one", changed) == baseline {
			t.Fatalf("approved field mutation %d did not change digest", i)
		}
	}
	if Digest("board-two", original) == baseline {
		t.Fatal("retargeting did not change digest")
	}
}

func TestDigestIgnoresJSONKeyOrderButPreservesApprovedText(t *testing.T) {
	var a, b Draft
	if err := json.Unmarshal([]byte(`{"title":"Fixture","body":"Line one\nLine two","type":"bug","origin":{"engine":"codex","repo":"fixture"}}`), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"origin":{"repo":"fixture","engine":"codex"},"type":"bug","body":"Line one\nLine two","title":"Fixture"}`), &b); err != nil {
		t.Fatal(err)
	}
	if Digest("board", a) != Digest("board", b) {
		t.Fatal("key order changed semantic digest")
	}
	b.Body += " "
	if Digest("board", a) == Digest("board", b) {
		t.Fatal("digest normalized approved text")
	}
}
