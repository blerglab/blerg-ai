package releasecut

import (
	"encoding/json"
	"testing"
)

func TestSnapshotDigestKeepsVersionZeroAndRejectsForeignMembership(t *testing.T) {
	card := SnapshotCard{Card: json.RawMessage(`{"id":"card","board_id":"board","release_id":"release","version":0}`), ColumnName: "Open"}
	digest, err := SnapshotDigest([]SnapshotCard{card}, "board", "release")
	if err != nil || digest != Digest([]CardVersion{{ID: "card", Version: 0, ColumnName: "Open"}}) {
		t.Fatalf("new card is valid evidence: %s %v", digest, err)
	}
	if _, err = SnapshotDigest([]SnapshotCard{card, card}, "board", "release"); err == nil {
		t.Fatal("duplicate evidence accepted")
	}
	if _, err = SnapshotDigest([]SnapshotCard{card}, "other", "release"); err == nil {
		t.Fatal("foreign membership accepted")
	}
}

func TestDigestBindsVersionsAndColumnClassification(t *testing.T) {
	a := CardVersion{ID: "a", Version: 0, ColumnName: "Open"}
	b := CardVersion{ID: "b", Version: 3, ColumnName: "Done", Terminal: true}
	original := Digest([]CardVersion{a, b})
	if Digest([]CardVersion{b, a}) != original {
		t.Fatal("query order changed the subject")
	}
	for _, changed := range []CardVersion{{ID: "b", Version: 4, ColumnName: "Done", Terminal: true}, {ID: "b", Version: 3, ColumnName: "Done"}, {ID: "b", Version: 3, ColumnName: "Renamed", Terminal: true}} {
		if Digest([]CardVersion{a, changed}) == original {
			t.Fatal("a card change did not invalidate authorization")
		}
	}
	for _, raw := range []string{`{"id":"a","board_id":"board","release_id":"release"}`, `{"id":"a","board_id":"board","release_id":"release","version":-1}`, `{"id":"a","board_id":"board","release_id":"other","version":0}`} {
		if _, err := SnapshotDigest([]SnapshotCard{{Card: json.RawMessage(raw)}}, "board", "release"); err == nil {
			t.Fatal("incomplete or foreign evidence accepted")
		}
	}
}
