package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Run the real terminal against a local fixture provider. No login or remote
// model is used; this verifies the native history identity and resume command.
func TestCodexTerminalNativeHistoryResumes(t *testing.T) {
	if os.Getenv("BLERG_REAL_CODEX") != "1" {
		t.Skip("set BLERG_REAL_CODEX=1 for native terminal verification")
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan struct{}, 8)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests <- struct{}{}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"terminal_fixture\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fixture_message\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Fixture reply.\"}]}}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"terminal_fixture\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer provider.Close()
	source, work := t.TempDir(), t.TempDir()
	config := fmt.Sprintf("model = \"fixture-model\"\nmodel_provider = \"fixture\"\n[model_providers.fixture]\nname = \"fixture\"\nbase_url = %q\nwire_api = \"responses\"\n[projects.%q]\ntrust_level = \"trusted\"\n", provider.URL+"/v1", work)
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	home, err := prepareCodexTerminalHome(t.TempDir(), "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = work
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + source, "CODEX_HOME=" + home, "TERM=xterm-256color"}
		terminal, err := pty.Start(cmd)
		if err != nil {
			t.Fatal(err)
		}
		defer terminal.Close()
		go func() { _, _ = io.Copy(io.Discard, terminal) }()
		select {
		case <-requests:
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("native terminal did not reach the local fixture provider")
		}
		// Allow the terminal to persist the completed turn before simulating loss.
		time.Sleep(250 * time.Millisecond)
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	run("--no-daemon", "--no-alt-screen", "--ask-for-approval", "never", "fixture kickoff")
	thread := codexTerminalThread(home)
	if thread == "" {
		t.Fatal("native terminal history was not identifiable")
	}
	run("--no-daemon", "--no-alt-screen", "--ask-for-approval", "never", "resume", thread, "fixture continuation")
	if got := codexTerminalThread(home); got != thread {
		t.Fatal("resume replaced the native conversation identity")
	}
}
