package updatehandoff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlannedUpdateConsumedOnce(t *testing.T) {
	for _, name := range []string{"success", "expired", "future", "wrong binary", "wrong commit", "unplanned", "malformed", "public file"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "daemon")
			if err := os.WriteFile(binary, []byte("replacement daemon"), 0600); err != nil {
				t.Fatal(err)
			}
			digest, err := Digest(binary)
			if err != nil {
				t.Fatal(err)
			}
			r := Receipt{Request: Request{SessionID: "s1", Call: json.RawMessage(`{"call_id":"update-1"}`)}, CreatedAt: time.Now(), BinarySHA256: digest, Commit: "abc1234"}
			planned := true
			switch name {
			case "expired":
				r.CreatedAt = time.Now().Add(-MaxAge)
			case "future":
				r.CreatedAt = time.Now().Add(time.Minute)
			case "wrong binary":
				r.BinarySHA256 = "old binary"
			case "wrong commit":
				r.Commit = "different"
			case "unplanned":
				planned = false
			}
			if err := saveJSON(dir, pendingFile, r); err != nil {
				t.Fatal(err)
			}
			if name == "malformed" {
				_ = os.WriteFile(filepath.Join(dir, pendingFile), []byte("{"), 0600)
			}
			if name == "public file" {
				_ = os.Chmod(filepath.Join(dir, pendingFile), 0644)
			}
			got := Take(dir, binary, "abc1234", planned)
			if (got != nil) != (name == "success") {
				t.Fatalf("evidence=%+v", got)
			}
			if again := Take(dir, binary, "abc1234", true); again != nil {
				t.Fatal("update replayed on a later restart")
			}
			if _, err := os.Lstat(filepath.Join(dir, pendingFile)); !os.IsNotExist(err) {
				t.Fatal("pending update not consumed", err)
			}
		})
	}
}

func TestUpdateFailureClearsTarget(t *testing.T) {
	dir := t.TempDir()
	o := &Observer{}
	r := Receipt{Commit: "abc1234"}
	if err := saveJSON(dir, pendingFile, r); err != nil {
		t.Fatal(err)
	}
	if err := o.prepare(dir, os.Getpid(), wireRequest{Cancel: true, Commit: "older"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, pendingFile)); err != nil {
		t.Fatal("older failure erased newer update")
	}
	if err := o.prepare(dir, os.Getpid(), wireRequest{Cancel: true, Commit: r.Commit}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, pendingFile)); !os.IsNotExist(err) {
		t.Fatal("failed update retained", err)
	}
}
