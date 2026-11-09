package liveaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSQLPredicatesRespectQuotesNestingAndConflict(t *testing.T) {
	sql := `INSERT INTO grants VALUES ('WHERE fake=true') ON CONFLICT(id) DO UPDATE SET active=true WHERE owner=$1 AND (enabled OR scope=$2) RETURNING id`
	got := sqlPredicates(sql)
	want := []string{"owner=$1", "(enabled OR scope=$2)", "scope=$2"}
	if len(got) != len(want) {
		t.Fatalf("predicates: %+v", got)
	}
	for i, s := range got {
		if s.value != want[i] || sql[s.a:s.b] != want[i] {
			t.Errorf("span %d: %+v want %s", i, s, want[i])
		}
	}
}
func TestClientInventorySkipsQuotedRegexAndComments(t *testing.T) {
	raw := "const emoji = '☀'; const r = /[\\\\/:*?\"<>|]+/g; // if (false)\nif (/*ignored*/ !rendered || !payloadExportAllowed(kind)) return;"
	g, e := webPredicates("VisualViewer.tsx", raw)
	if e != nil {
		t.Fatal(e)
	}
	if len(g) != 1 || str(g[0], "expression") != "!rendered || !payloadExportAllowed(kind)" {
		t.Fatalf("lost export refusal after regex: %+v", g)
	}
	a, b := num(g[0], "start"), num(g[0], "end")
	if raw[a:b] != str(g[0], "expression") {
		t.Fatal("offsets are not UTF-8 byte offsets")
	}
}
func TestClientRuntimeFailureIsNeverKilled(t *testing.T) {
	data := Object{"testResults": []Object{{"assertionResults": []Object{{"status": "failed", "fullName": "guard", "failureMessages": []string{"AssertionError: expected refusal; TypeError: undefined"}}}}}}
	o := webObservation(data, 1, 0)
	if got := webOutcome(o); got != "harness_or_runtime_error" {
		t.Fatalf("runtime falsely certified: %s", got)
	}
	data["testResults"] = []Object{{"assertionResults": []Object{{"status": "failed", "fullName": "guard", "failureMessages": []string{"expect(element).not.toBeVisible()"}}}}}
	if got := webOutcome(webObservation(data, 1, 0)); got != "killed" {
		t.Fatalf("DOM assertion not recognized: %s", got)
	}
}
func TestSummaryProvenanceAndMissingEvidence(t *testing.T) {
	root := t.TempDir()
	if e := os.WriteFile(filepath.Join(root, "guard.go"), []byte("guard"), 0600); e != nil {
		t.Fatal(e)
	}
	g := Object{"id": "live-one", "file": "guard.go", "source_sha256": digest([]byte("guard")), "start": 0, "suite": []string{"a"}}
	catalog := filepath.Join(root, "catalog.json")
	record := filepath.Join(root, "record.json")
	output := filepath.Join(root, "summary.json")
	write := func(path string, m Object) {
		t.Helper()
		if e := save(path, m); e != nil {
			t.Fatal(e)
		}
	}
	write(catalog, Object{"guards": []Object{g}})
	old := clone(g)
	old["status"] = "survived"
	write(record, Object{"base_commit": "old", "method": "probe", "guards": []Object{old}})
	latest := filepath.Join(root, "latest.json")
	newer := clone(g)
	newer["status"] = "killed"
	write(latest, Object{"base_commit": "new", "method": "probe", "guards": []Object{newer}})
	code, e := Summarize(root, []string{catalog}, []string{record, latest}, output)
	if e != nil || code != 0 {
		t.Fatalf("summary: %d %v", code, e)
	}
	d, e := load(output)
	if e != nil {
		t.Fatal(e)
	}
	row := rows(d, "guards")[0]
	if str(row, "status") != "killed" || str(row, "proof_commit") != "new" || len(rows(row, "earlier_observations")) != 1 {
		t.Fatalf("lost history: %+v", row)
	}
	newer["source_sha256"] = "stale"
	old["source_sha256"] = "stale"
	write(record, Object{"base_commit": "old", "method": "probe", "guards": []Object{old}})
	code, e = Summarize(root, []string{catalog}, []string{record}, output)
	if e != nil || code != 1 {
		t.Fatalf("missing proof accepted: %d %v", code, e)
	}
	g["source_sha256"] = "stale"
	write(catalog, Object{"guards": []Object{g}})
	if _, e = Summarize(root, []string{catalog}, []string{record}, output); e == nil {
		t.Fatal("stale catalogue accepted")
	}
}
func TestTaggedSwitchKeepsUnselectedSibling(t *testing.T) {
	root, tree := t.TempDir(), t.TempDir()
	raw := `package fixture
func f(n int) int { switch n {case 1: return 11; case 2: return 22}; return 0 }
`
	for _, d := range []string{root, tree} {
		if e := os.MkdirAll(filepath.Join(d, "fixture"), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(d, "fixture/x.go"), []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
	}
	tag := strings.Index(raw, "switch n") + len("switch ")
	cases := []Object{}
	for _, n := range []string{"1", "2"} {
		a := strings.Index(raw, "case "+n) + len("case ")
		cases = append(cases, Object{"file": "fixture/x.go", "id": "case-" + n, "kind": "case", "tag": "n", "tag_start": tag, "tag_end": tag + 1, "start": a, "end": a + 1})
	}
	if e := instrument(root, tree, cases[:1], cases); e != nil {
		t.Fatal(e)
	}
	text, e := os.ReadFile(filepath.Join(tree, "fixture/x.go"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(text), `blergMutationGuard("case-2")`) {
		t.Fatal("unselected case not transformed alongside switch")
	}
	if !strings.Contains(string(text), "true/*n*/") {
		t.Fatal("tag not preserved for baseline comparison")
	}
}
func TestSelectionRejectsUnknownIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ids.json")
	b, _ := json.Marshal([]string{"unknown"})
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := selectGuards([]Object{{"id": "known", "file": "a"}}, "", path); e == nil {
		t.Fatal("unknown reproof silently ignored")
	}
}
