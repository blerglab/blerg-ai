package desktopupgrade

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in drill uses a private, disposable Compose project and real PostgreSQL
// dumps/migrations/restores. Host daemon and application installation remain
// simulated by the orchestration fixture; it never contacts an installed stack.
func disposableDatabaseDrill(t *testing.T, root, daemonState string, u *Upgrader) (func(context.Context, string, io.Writer) error, func(Operation)) {
	t.Helper()
	project := fmt.Sprintf("blerg-upgrade-drill-%d", time.Now().UnixNano())
	compose := filepath.Join(root, "install/desktop/compose.drill.yml")
	raw := "services:\n  postgres:\n    image: postgres:16\n    environment:\n      POSTGRES_USER: blerg\n      POSTGRES_HOST_AUTH_METHOD: trust\n    tmpfs:\n      - /var/lib/postgresql/data\n"
	if err := os.WriteFile(compose, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(compose) })
	// The ignored fixture file must not make the upgrade checkout dirty.
	f, err := os.OpenFile(filepath.Join(root, ".git/info/exclude"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\ninstall/desktop/compose.drill.yml\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	command := func(ctx context.Context, args ...string) *exec.Cmd {
		base := []string{"compose", "-p", project, "-f", compose}
		return exec.CommandContext(ctx, "docker", append(base, args...)...)
	}
	run := func(args ...string) string {
		t.Helper()
		out, e := command(t.Context(), args...).CombinedOutput()
		if e != nil {
			t.Fatalf("disposable database command failed: %v: %s", e, out)
		}
		return string(out)
	}
	run("up", "-d")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if out, e := command(ctx, "down", "--volumes").CombinedOutput(); e != nil {
			t.Errorf("drill cleanup: %v: %s", e, out)
		}
	})
	ready := false
	for i := 0; i < 100; i++ {
		if command(t.Context(), "exec", "-T", "postgres", "pg_isready", "-U", "blerg").Run() == nil {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("drill database did not become ready")
	}
	sql := func(db, query string) string {
		return run("exec", "-T", "postgres", "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "blerg", "-d", db, "-Atc", query)
	}
	for _, db := range []string{"blerg_core", "blerg_board", "blerg_runner"} {
		run("exec", "-T", "postgres", "createdb", "-U", "blerg", db)
		sql(db, "CREATE TABLE drill (value text); INSERT INTO drill VALUES ('before')")
	}
	prior := u.Run
	u.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "env" {
			for _, db := range []string{"blerg_core", "blerg_board", "blerg_runner"} {
				sql(db, "ALTER TABLE drill ADD COLUMN migrated boolean; UPDATE drill SET value='after'")
			}
		}
		return prior(ctx, name, args...)
	}
	dump := func(ctx context.Context, db string, w io.Writer) error {
		cmd := command(ctx, "exec", "-T", "postgres", "pg_dump", "-U", "blerg", "-d", db)
		cmd.Stdout = w
		return cmd.Run()
	}
	restore := func(op Operation) {
		for _, db := range []string{"blerg_core", "blerg_board", "blerg_runner"} {
			if strings.TrimSpace(sql(db, "SELECT value FROM drill")) != "after" {
				t.Fatal("migration did not run")
			}
			sql(db, "DROP SCHEMA public CASCADE; CREATE SCHEMA public")
			f, e := os.Open(filepath.Join(op.Backup, db+".sql"))
			if e != nil {
				t.Fatal(e)
			}
			cmd := command(t.Context(), "exec", "-T", "postgres", "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "blerg", "-d", db)
			cmd.Stdin = f
			out, e := cmd.CombinedOutput()
			_ = f.Close()
			if e != nil {
				t.Fatalf("restore failed: %v: %s", e, out)
			}
			if strings.TrimSpace(sql(db, "SELECT value FROM drill")) != "before" {
				t.Fatal("restore did not recover old data")
			}
			if strings.TrimSpace(sql(db, "SELECT count(*) FROM information_schema.columns WHERE table_name='drill' AND column_name='migrated'")) != "0" {
				t.Fatal("restore retained migrated schema")
			}
		}
		credential := filepath.Join(daemonState, "desktop-credential.json")
		if e := os.WriteFile(credential, []byte("changed"), 0600); e != nil {
			t.Fatal(e)
		}
		if e := copyPrivate(filepath.Join(op.Backup, "desktop-credential.json"), credential, false); e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(credential)
		if e != nil || string(b) != "private-test-credential" {
			t.Fatal("credential restore failed")
		}
	}
	return dump, restore
}
