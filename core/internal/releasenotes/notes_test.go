package releasenotes

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestReleaseNotesAreBoundedAndOffline(t *testing.T) {
	notes, err := Through("0.6.0")
	if err != nil || len(notes) != 2 {
		t.Fatalf("notes = %v, %v", notes, err)
	}
	body, err := json.Marshal(notes)
	if err != nil || len(body) > MaxBytes {
		t.Fatalf("notes size: %d, %v", len(body), err)
	}
	for _, v := range []string{"", "dev", "0.6.0-private", "0.6", "01.6.0"} {
		if Valid(v) {
			t.Errorf("accepted %q", v)
		}
		notes, err := Through(v)
		if err != nil || len(notes) != 0 {
			t.Errorf("%q notes = %v, %v", v, notes, err)
		}
	}
	if Compare("0.9.0", "0.10.0") >= 0 || Compare("v0.6.0", "0.6.0") != 0 {
		t.Fatal("semantic comparison")
	}
}

func TestVersionHasOwnReleaseNotes(t *testing.T) {
	raw, err := os.ReadFile("../../../VERSION")
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(raw))
	if _, err := files.ReadFile("notes/" + version + ".json"); err != nil {
		t.Fatalf("VERSION %s has no notes file: %v", version, err)
	}
	releases, err := Through(version)
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range releases {
		if release.Version == version {
			return
		}
	}
	t.Fatalf("VERSION %s has no release notes file", version)
}
