package db_test

import (
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"testing"
)

func TestShellNamesPersistWithExactOwnerLoginAndSession(t *testing.T) {
	pool := connect(t)
	ctx := t.Context()
	const desktop = "00000000-0000-0000-0000-000000000001"
	const session = "00000000-0000-0000-0000-000000000002"
	if err := db.UpsertDaemon(ctx, pool, desktop, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, desktop, "running", "/repos/one", "org/one", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveShellName(ctx, pool, session, "owner", "login", "shell", "Test watcher"); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveShellName(ctx, pool, session, "other", "login", "shell", "stolen"); err == nil {
		t.Fatal("name scope mismatch accepted")
	}
	for _, scope := range [][2]string{{"owner", "login"}, {"other", "login"}, {"owner", "other"}} {
		names, err := db.ShellNames(ctx, pool, session, scope[0], scope[1])
		if err != nil {
			t.Fatal(err)
		}
		if scope == [2]string{"owner", "login"} {
			if names["shell"] != "Test watcher" {
				t.Fatal(names)
			}
		} else if len(names) != 0 {
			t.Fatal("name leaked", names)
		}
	}
	if err := db.SaveShellName(ctx, pool, session, "owner", "login", "shell", ""); err != nil {
		t.Fatal(err)
	}
	names, err := db.ShellNames(ctx, pool, session, "owner", "login")
	if err != nil || len(names) != 0 {
		t.Fatal(names, err)
	}
}
