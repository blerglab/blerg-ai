package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/gorilla/websocket"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "modelprobe", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func modelIDs(ms []models.Model) []string {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return ids
}

// Every engine that renders effort has an allowlist to check it against, and
// every engine with a probe has rules for its ids.
func TestEngineSpecsEffortAndProbeHaveRules(t *testing.T) {
	for id, spec := range engineRegistry {
		if spec.EffortArgs != nil && len(models.EffortsFor(id)) == 0 {
			t.Errorf("%s has an EffortArgs hook but no effort levels in the models table", id)
		}
		if spec.ListModels != nil && !models.HasEngineRules(id) {
			t.Errorf("%s has a ListModels probe but no rules", id)
		}
	}
	if engineRegistry["codex"].ListModels == nil || engineRegistry["hermes"].ListModels == nil {
		t.Error("codex and hermes should both have a model probe")
	}
	if engineRegistry["openclaw"].ListModels != nil {
		t.Error("openclaw has no model list source")
	}
}

// ─── Codex ───────────────────────────────────────────────────────────────────

func TestParseCodexModels(t *testing.T) {
	got, err := parseCodexModels(readFixture(t, "codex_models.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Listed only (hidden gpt-reserve and codex-auto-review are out), by
	// priority with the priority-less one last; the hostile slugs and the
	// entry whose priority is not a number are dropped.
	if want := []string{"gpt-6-astra", "gpt-5.6-terra", "gpt-5.5", "gpt-5.6-luna"}; !slices.Equal(modelIDs(got), want) {
		t.Fatalf("ids = %v, want %v", modelIDs(got), want)
	}
	astra := got[0]
	if astra.Name != "GPT-6-Astra" || astra.Section != "main" || astra.EffortKind != "reasoning" || astra.DefaultEffort != "low" {
		t.Errorf("astra = %+v", astra)
	}
	// Both element shapes (bare strings here, objects for terra) are read.
	all := []string{"low", "medium", "high", "xhigh", "max", "ultra"}
	if !slices.Equal(astra.Efforts, all) || !slices.Equal(got[1].Efforts, all) {
		t.Errorf("efforts = %v / %v", astra.Efforts, got[1].Efforts)
	}
	// Unknown levels and odd shapes dropped; an unknown default becomes none;
	// control and bidi characters are stripped from the name.
	legacy := got[2]
	if !slices.Equal(legacy.Efforts, []string{"low", "medium", "high", "xhigh"}) || legacy.DefaultEffort != "" || legacy.Name != "GPT-5.5" {
		t.Errorf("gpt-5.5 = %+v", legacy)
	}
}

func TestParseCodexModelsRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "not json", `{"models": "x"}`, `{"other": []}`, `[]`} {
		if _, err := parseCodexModels([]byte(in)); err == nil {
			t.Errorf("%q: want an error", in)
		}
	}
	// An empty catalog is a valid (empty) answer.
	if got, err := parseCodexModels([]byte(`{"models": []}`)); err != nil || len(got) != 0 {
		t.Errorf("empty catalog: %v, %v", got, err)
	}
}

// writeScript puts an executable script named name in a fresh dir.
func writeScript(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestProbeCodexModelsRunsTheCLI(t *testing.T) {
	fixture, _ := filepath.Abs(filepath.Join("testdata", "modelprobe", "codex_models.json"))
	dir := writeScript(t, "codex", `
[ "$1 $2" = "debug models" ] || exit 3
[ -t 0 ] && exit 4
env > "$(dirname "$0")/env.txt"
cat "`+fixture+`"
`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("BLERG_RUNNER_DAEMON_TOKEN", "master-secret")
	t.Setenv("OPENAI_API_KEY", "sk-secret")
	got, err := probeCodexModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Errorf("got %v", modelIDs(got))
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env.txt"))
	if strings.Contains(string(env), "secret") {
		t.Errorf("the probe leaked the daemon's environment:\n%s", env)
	}
	if !strings.Contains(string(env), "PATH=") || !strings.Contains(string(env), "HOME=") {
		t.Errorf("the probe needs PATH and HOME:\n%s", env)
	}
}

func TestProbeCodexModelsNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := probeCodexModels(context.Background()); !errors.Is(err, errNoModelList) {
		t.Errorf("err = %v, want errNoModelList", err)
	}
}

// A hung CLI is killed at the deadline — with its whole process group, so a
// child it spawned does not outlive it.
func TestRunProbeCommandTimeoutKillsTheGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "child-survived")
	dir := writeScript(t, "hang", `(sleep 2; touch "`+marker+`") & sleep 30`)
	start := time.Now()
	_, err := runProbeCommand(context.Background(), 200*time.Millisecond, 1024, filepath.Join(dir, "hang"))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v", d)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the probe's child process survived the kill")
	}
}

func TestRunProbeCommandCapsOutput(t *testing.T) {
	dir := writeScript(t, "loud", `head -c 5000000 /dev/zero; sleep 30`)
	start := time.Now()
	_, err := runProbeCommand(context.Background(), 10*time.Second, 1<<20, filepath.Join(dir, "loud"))
	if !errors.Is(err, errProbeOutputTooLarge) {
		t.Fatalf("err = %v, want errProbeOutputTooLarge", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("an oversized probe was not killed promptly (%v)", d)
	}
}

// ─── Hermes ──────────────────────────────────────────────────────────────────

func TestReadHermesModelConfig(t *testing.T) {
	cfg, err := readHermesModelConfig(filepath.Join("testdata", "modelprobe", "hermes_config_custom.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != (hermesModelConfig{Provider: "custom", BaseURL: "BASE_URL", Default: "qwen3-30b"}) {
		t.Errorf("custom = %+v", cfg)
	}
	cfg, _ = readHermesModelConfig(filepath.Join("testdata", "modelprobe", "hermes_config_nous.yaml"))
	if cfg.Provider != "nous" {
		t.Errorf("nous = %+v", cfg)
	}
	// model: as a scalar has no provider; keys outside model: are not it.
	cfg, _ = readHermesModelConfig(filepath.Join("testdata", "modelprobe", "hermes_config_scalar.yaml"))
	if cfg != (hermesModelConfig{}) {
		t.Errorf("scalar = %+v", cfg)
	}
	if _, err := readHermesModelConfig(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("a missing config should be an error")
	}
}

func TestYAMLScalar(t *testing.T) {
	for in, want := range map[string]string{
		" custom":                  "custom",
		` "a # b"  # c`:            "a # b",
		" 'x'":                     "x",
		" 'it''s'":                 "it's",
		` "q\"uote\\d\/"`:          `q"uote\d/`,
		" http://h:8000/v1 # lan":  "http://h:8000/v1",
		" http://h:8000/v1\t# lan": "http://h:8000/v1",
		" http://h/#frag":          "http://h/#frag", // no space before #: not a comment
		"":                         "",
		" # only a comment":        "",
	} {
		if got, ok := yamlScalar(in); !ok || got != want {
			t.Errorf("yamlScalar(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	// What this parser cannot read exactly is refused, never guessed.
	for _, in := range []string{" [a, b]", " {provider: custom}", " |", " >-", " &anchor x", " *alias", " !!str x",
		` "unterminated`, " 'unterminated", ` "tab\tescape\n"`, ` "\e"`, ` "x" trailing`, " 'x' y"} {
		if got, ok := yamlScalar(in); ok {
			t.Errorf("yamlScalar(%q) = %q, accepted", in, got)
		}
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A config that exists but cannot be read exactly is an error (logged, last
// list kept) — only a missing one is a quiet "no list".
func TestHermesConfigProblemsAreErrors(t *testing.T) {
	client := newHermesProbeClient()
	for name, body := range map[string]string{
		"long line":     "model:\n  provider: custom\n  base_url: http://h/v1\n# " + strings.Repeat("x", 70<<10) + "\n",
		"flow model":    "model: {provider: custom, base_url: http://h/v1}\n",
		"bad escape":    "model:\n  provider: custom\n  base_url: \"http://h\\x41/v1\"\n",
		"trailing text": "model:\n  provider: 'custom' extra\n",
	} {
		_, err := probeHermesModelsAt(context.Background(), writeConfig(t, body), client)
		if err == nil || errors.Is(err, errNoModelList) {
			t.Errorf("%s: err = %v, want a (non-no-list) error", name, err)
		}
	}
	big := writeConfig(t, "model:\n  provider: custom\n"+strings.Repeat("# pad\n", (hermesConfigMaxRead/6)+10))
	if _, err := readHermesModelConfig(big); err == nil {
		t.Error("an oversized config should be an error")
	}
	if _, err := probeHermesModelsAt(context.Background(), filepath.Join(t.TempDir(), "missing.yaml"), client); !errors.Is(err, errNoModelList) {
		t.Errorf("missing config: %v, want errNoModelList", err)
	}
	// Unrelated keys (the api_key included) are not even parsed: an odd value
	// there changes nothing.
	cfg, err := readHermesModelConfig(writeConfig(t, "model:\n  api_key: \"sk-\\q\"\n  provider: custom\n  base_url: http://h/v1\n"))
	if err != nil || cfg.Provider != "custom" {
		t.Errorf("cfg %+v, %v", cfg, err)
	}
}

// A FIFO (or any non-regular file) in place of the config must not block.
func TestHermesConfigFIFODoesNotBlock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := syscall.Mkfifo(p, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readHermesModelConfig(p)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a FIFO config should be an error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reading a FIFO config blocked")
	}
	// Symlinked dotfiles still work.
	target := writeConfig(t, "model:\n  provider: custom\n  base_url: http://h/v1\n")
	link := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if cfg, err := readHermesModelConfig(link); err != nil || cfg.BaseURL != "http://h/v1" {
		t.Errorf("symlinked config: %+v, %v", cfg, err)
	}
}

// Codex exiting non-zero on its own (logged out, no such subcommand) is a
// definite "no list"; our own kill at the deadline is transient.
func TestProbeCodexExitVersusKill(t *testing.T) {
	dir := writeScript(t, "codex", `echo "Error: not logged in" >&2; exit 1`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if _, err := probeCodexModels(context.Background()); !errors.Is(err, errNoModelList) {
		t.Errorf("non-zero exit: %v, want errNoModelList", err)
	}
	dir = writeScript(t, "codex", `sleep 30`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := probeCodexModels(ctx)
	if err == nil || errors.Is(err, errNoModelList) {
		t.Errorf("killed at the deadline: %v, want a transient error", err)
	}
}

// openAIServer serves body at /v1/models with status and records requests.
func openAIServer(t *testing.T, status int, body []byte) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	var mu sync.Mutex
	var reqs []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r)
		mu.Unlock()
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func TestFetchOpenAIModels(t *testing.T) {
	srv, reqs := openAIServer(t, http.StatusOK, readFixture(t, "openai_models.json"))
	got, err := fetchOpenAIModels(context.Background(), newHermesProbeClient(), srv.URL+"/v1/", "qwen3-30b")
	if err != nil {
		t.Fatal(err)
	}
	// The configured default leads; then the server's order.
	if want := []string{"qwen3-30b", "llama-3.3-70b", "mistral-small", "phi-4", "gemma-3-27b"}; !slices.Equal(modelIDs(got), want) {
		t.Fatalf("ids = %v, want %v", modelIDs(got), want)
	}
	for i, m := range got {
		if wantSec := map[bool]string{true: "main", false: "overflow"}[i < hermesMainModels]; m.Section != wantSec {
			t.Errorf("%s section = %q, want %q", m.ID, m.Section, wantSec)
		}
		// Hermes does not report per-model support: every level, no default.
		if !slices.Equal(m.Efforts, models.EffortsFor("hermes")) || m.DefaultEffort != "" || m.EffortKind != "reasoning" {
			t.Errorf("%s = %+v", m.ID, m)
		}
	}
	if got[0].Description != "65536-token context" || got[2].Description != "" || got[3].Description != "" {
		t.Errorf("descriptions: %q %q %q", got[0].Description, got[2].Description, got[3].Description)
	}
	r := (*reqs)[0]
	if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" {
		t.Errorf("request = %s %s auth=%q", r.Method, r.URL, r.Header.Get("Authorization"))
	}
}

func TestFetchOpenAIModelsDropsHostileIDs(t *testing.T) {
	srv, _ := openAIServer(t, http.StatusOK, readFixture(t, "openai_models_hostile.json"))
	got, err := fetchOpenAIModels(context.Background(), newHermesProbeClient(), srv.URL+"/v1", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ok-model", "org/model:tag@v1"}; !slices.Equal(modelIDs(got), want) {
		t.Errorf("ids = %v, want %v", modelIDs(got), want)
	}
}

func TestFetchOpenAIModelsFailures(t *testing.T) {
	big := []byte(`{"data":[` + strings.Repeat(`{"id":"m"},`, 30000) + `{"id":"m"}]}`)
	for _, tc := range []struct {
		name   string
		status int
		body   []byte
		noList bool // errNoModelList rather than a transient error
	}{
		{"401", http.StatusUnauthorized, []byte(`{"error":"key"}`), true},
		{"403", http.StatusForbidden, nil, true},
		{"500", http.StatusInternalServerError, nil, false},
		{"non-JSON", http.StatusOK, []byte("<html>hello</html>"), false},
		{"no data", http.StatusOK, []byte(`{"models":[]}`), false},
		{"oversized", http.StatusOK, big, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := openAIServer(t, tc.status, tc.body)
			_, err := fetchOpenAIModels(context.Background(), newHermesProbeClient(), srv.URL+"/v1", "")
			if err == nil {
				t.Fatal("want an error")
			}
			if errors.Is(err, errNoModelList) != tc.noList {
				t.Errorf("err = %v, noList want %v", err, tc.noList)
			}
		})
	}
}

// A redirect is never followed — not even to the same server.
func TestFetchOpenAIModelsDoesNotFollowRedirects(t *testing.T) {
	var hit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		_, _ = w.Write([]byte(`{"data":[{"id":"x"}]}`))
	}))
	t.Cleanup(target.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/models", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	if _, err := fetchOpenAIModels(context.Background(), newHermesProbeClient(), srv.URL+"/v1", ""); err == nil {
		t.Error("a redirect should be an error")
	}
	if hit.Load() {
		t.Error("the redirect was followed")
	}
}

func TestFetchOpenAIModelsRefusesURLs(t *testing.T) {
	srv, reqs := openAIServer(t, http.StatusOK, []byte(`{"data":[]}`))
	withCreds := strings.Replace(srv.URL, "http://", "http://user:pass@", 1) + "/v1"
	for _, u := range []string{withCreds, "ftp://host/v1", "file:///etc/passwd", "/v1", "://bad", "javascript:alert(1)"} {
		if _, err := fetchOpenAIModels(context.Background(), newHermesProbeClient(), u, ""); !errors.Is(err, errNoModelList) {
			t.Errorf("%q: err = %v, want errNoModelList", u, err)
		}
	}
	if len(*reqs) != 0 {
		t.Error("a refused URL was requested")
	}
}

func TestFetchOpenAIModelsTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := fetchOpenAIModels(ctx, newHermesProbeClient(), srv.URL+"/v1", ""); err == nil || errors.Is(err, errNoModelList) {
		t.Errorf("err = %v, want a transient error", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestProbeHermesModelsFromConfig(t *testing.T) {
	srv, reqs := openAIServer(t, http.StatusOK, readFixture(t, "openai_models.json"))
	cfg := strings.Replace(string(readFixture(t, "hermes_config_custom.yaml")), "BASE_URL", srv.URL+"/v1", 1)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := probeHermesModelsAt(context.Background(), path, newHermesProbeClient())
	if err != nil || len(got) != 5 || got[0].ID != "qwen3-30b" {
		t.Fatalf("got %v, %v", modelIDs(got), err)
	}
	for _, r := range *reqs {
		for k, v := range r.Header {
			if strings.Contains(strings.Join(v, ","), "sk-") {
				t.Errorf("the api key was sent in %s", k)
			}
		}
	}
	for _, name := range []string{"hermes_config_nous.yaml", "hermes_config_scalar.yaml", "no-such.yaml"} {
		if _, err := probeHermesModelsAt(context.Background(), filepath.Join("testdata", "modelprobe", name), newHermesProbeClient()); !errors.Is(err, errNoModelList) {
			t.Errorf("%s: err = %v, want errNoModelList", name, err)
		}
	}
}

// ─── Prober ──────────────────────────────────────────────────────────────────

type fakeProbe struct {
	mu    sync.Mutex
	calls int
	ms    []models.Model
	err   error
	block chan struct{} // when set, the probe waits on it (or its ctx)
}

func (f *fakeProbe) probe(ctx context.Context) ([]models.Model, error) {
	f.mu.Lock()
	f.calls++
	ms, err, block := f.ms, f.err, f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return ms, err
}

func (f *fakeProbe) set(ms []models.Model, err error) {
	f.mu.Lock()
	f.ms, f.err = ms, err
	f.mu.Unlock()
}

func codexModel(id string) models.Model {
	return models.Model{ID: id, Name: id, Section: "main", Efforts: []string{"high", "turbo"}}
}

func TestModelProberReportsOnChangeOnly(t *testing.T) {
	codex := &fakeProbe{ms: []models.Model{codexModel("gpt-a")}}
	hermes := &fakeProbe{err: errNoModelList}
	p := newModelProber(map[string]modelProbe{"codex": codex.probe, "hermes": hermes.probe})
	if snap, gen := p.Snapshot(); snap != nil || gen != 0 {
		t.Fatalf("before any probe: %v %d", snap, gen)
	}
	p.ProbeAll(context.Background())
	snap, gen1 := p.Snapshot()
	if gen1 == 0 {
		t.Fatal("first probe should move the generation")
	}
	// Sanitized on the daemon side too: the unknown effort is gone.
	if got := snap["codex"].Models; len(got) != 1 || !slices.Equal(got[0].Efforts, []string{"high"}) {
		t.Errorf("codex = %+v", got)
	}
	// errNoModelList: reported, as an empty list.
	if r, ok := snap["hermes"]; !ok || r.Models == nil || len(r.Models) != 0 {
		t.Errorf("hermes = %+v (present %v)", r, ok)
	}
	select {
	case <-p.Changed():
	default:
		t.Error("Changed not signalled")
	}

	// Same result again: no new generation, no signal.
	p.ProbeAll(context.Background())
	if _, gen := p.Snapshot(); gen != gen1 {
		t.Errorf("an unchanged probe moved the generation %d → %d", gen1, gen)
	}
	select {
	case <-p.Changed():
		t.Error("Changed signalled without a change")
	default:
	}

	// A transient failure keeps the last good list.
	codex.set(nil, errors.New("timeout"))
	p.ProbeAll(context.Background())
	snap, gen := p.Snapshot()
	if gen != gen1 || len(snap["codex"].Models) != 1 {
		t.Errorf("a failed probe dropped the list: gen %d, %+v", gen, snap["codex"])
	}

	// A definite "nothing" replaces it.
	codex.set(nil, errNoModelList)
	p.ProbeAll(context.Background())
	snap, gen = p.Snapshot()
	if gen == gen1 || len(snap["codex"].Models) != 0 {
		t.Errorf("errNoModelList should empty the list: gen %d, %+v", gen, snap["codex"])
	}

	// Snapshots are copies.
	codex.set([]models.Model{codexModel("gpt-b")}, nil)
	p.ProbeAll(context.Background())
	snap, _ = p.Snapshot()
	snap["codex"].Models[0].Efforts[0] = "mutated"
	again, _ := p.Snapshot()
	if again["codex"].Models[0].Efforts[0] != "high" {
		t.Error("Snapshot shares memory with the prober")
	}
}

func TestModelProberFirstFailureReportsEmpty(t *testing.T) {
	codex := &fakeProbe{err: errors.New("exit status 1")}
	p := newModelProber(map[string]modelProbe{"codex": codex.probe})
	p.ProbeAll(context.Background())
	if snap, gen := p.Snapshot(); gen == 0 || len(snap["codex"].Models) != 0 {
		t.Errorf("snap %v gen %d", snap, gen)
	}
}

// A probe that ignores its context is abandoned at the deadline (plus grace)
// instead of stalling the others, keeps its last list, and is not started
// again while the abandoned run is still going.
func TestModelProberAbandonsAHungProbe(t *testing.T) {
	release := make(chan struct{})
	var hungCalls atomic.Int32
	hung := func(context.Context) ([]models.Model, error) { // ignores ctx
		hungCalls.Add(1)
		<-release
		return []models.Model{codexModel("late")}, nil
	}
	hermesOK := &fakeProbe{ms: []models.Model{{ID: "qwen3-30b", Efforts: []string{"none"}}}}
	p := newModelProber(map[string]modelProbe{"codex": hung, "hermes": hermesOK.probe})
	p.deadline, p.grace = 100*time.Millisecond, 50*time.Millisecond

	start := time.Now()
	p.ProbeAll(context.Background())
	if d := time.Since(start); d > time.Second {
		t.Fatalf("ProbeAll waited %v for a hung probe", d)
	}
	snap, _ := p.Snapshot()
	if len(snap["hermes"].Models) != 1 {
		t.Errorf("the healthy probe's result was lost: %+v", snap)
	}
	if r, ok := snap["codex"]; !ok || len(r.Models) != 0 {
		t.Errorf("abandoned first probe: %+v", r)
	}
	// Still hung: not started a second time.
	p.ProbeAll(context.Background())
	if n := hungCalls.Load(); n != 1 {
		t.Errorf("a still-running probe was started again (%d calls)", n)
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.mu.Lock()
		busy := p.inflight["codex"]
		p.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the abandoned probe never cleared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.ProbeAll(context.Background())
	if n := hungCalls.Load(); n != 2 {
		t.Errorf("calls = %d after the probe returned", n)
	}
}

// Linear, deduplicated parsing: the preferred model's first occurrence leads
// even when it comes after the cap, and duplicates never take a slot.
func TestParseOpenAIModelsDedupesAndPromotes(t *testing.T) {
	var entries []string
	for i := range 300 {
		entries = append(entries, fmt.Sprintf(`{"id":"m-%d"}`, i%100)) // every id three times
	}
	entries = append(entries, `{"id":"--bad"}`, `{"id":"wanted","max_model_len":8192}`, `{"id":"wanted"}`)
	got, err := parseOpenAIModels([]byte(`{"data":[`+strings.Join(entries, ",")+`]}`), "wanted")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != models.MaxReportedModels || got[0].ID != "wanted" || got[0].Description != "8192-token context" || got[1].ID != "m-0" {
		t.Fatalf("got %d: %v", len(got), modelIDs(got)[:3])
	}
	seen := map[string]bool{}
	for _, m := range got {
		if seen[m.ID] {
			t.Fatalf("duplicate %s", m.ID)
		}
		seen[m.ID] = true
	}
}

// Run probes at start, honours Kick only once minGap has passed, and a
// probe's deadline bounds it.
func TestModelProberRunKickAndDeadline(t *testing.T) {
	codex := &fakeProbe{ms: []models.Model{codexModel("gpt-a")}}
	slow := &fakeProbe{block: make(chan struct{})} // never answers
	p := newModelProber(map[string]modelProbe{"codex": codex.probe, "hermes": slow.probe})
	p.interval, p.jitter = time.Hour, 0
	p.deadline = 100 * time.Millisecond
	p.minGap = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	calls := func() int { codex.mu.Lock(); defer codex.mu.Unlock(); return codex.calls }
	waitFor("the start probe", func() bool { _, g := p.Snapshot(); return g > 0 })
	if calls() != 1 {
		t.Fatalf("calls = %d", calls())
	}
	p.Kick() // within minGap: ignored
	time.Sleep(100 * time.Millisecond)
	if calls() != 1 {
		t.Errorf("a kick within minGap probed again (calls %d)", calls())
	}
	time.Sleep(300 * time.Millisecond)
	p.Kick()
	waitFor("the kicked probe", func() bool { return calls() == 2 })
}

// ─── WSClient reporting ──────────────────────────────────────────────────────

// fakeModelSource is an EngineModelSource under the test's control.
type fakeModelSource struct {
	mu      sync.Mutex
	snap    map[string]models.Report
	gen     uint64
	changed chan struct{}
}

func (f *fakeModelSource) Snapshot() (map[string]models.Report, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, f.gen
}

func (f *fakeModelSource) Changed() <-chan struct{} { return f.changed }

func (f *fakeModelSource) update(snap map[string]models.Report) {
	f.mu.Lock()
	f.snap = snap
	f.gen++
	f.mu.Unlock()
	f.changed <- struct{}{}
}

// The hello carries the lists; heartbeats carry them only after a change,
// and a change is reported at once rather than on the next tick.
func TestWSClientReportsEngineModelsOnConnectAndChange(t *testing.T) {
	type msg struct {
		typ    string
		models map[string]models.Report
		has    bool
	}
	msgs := make(chan msg, 64)
	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env struct {
				Type         string                   `json:"type"`
				EngineModels map[string]models.Report `json:"engine_models"`
			}
			_ = json.Unmarshal(raw, &env)
			var probe map[string]json.RawMessage
			_ = json.Unmarshal(raw, &probe)
			_, has := probe["engine_models"]
			msgs <- msg{env.Type, env.EngineModels, has}
		}
	})
	src := &fakeModelSource{changed: make(chan struct{}, 1), gen: 1, snap: map[string]models.Report{
		"codex": {Models: []models.Model{codexModel("gpt-a")}, FetchedAt: time.Now()},
	}}
	// An hour between ticks: any heartbeat seen in this test is one the
	// change signal sent, so its arrival within seconds proves immediacy.
	client := NewWSClient(Config{
		ServerURL: ts.wsURL(), DaemonName: "d", ProtocolVersion: "1",
		HeartbeatInterval: time.Hour,
	})
	client.SetEngineModels(src)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go client.Run(ctx, func() []string { return nil })

	next := func() msg {
		t.Helper()
		select {
		case m := <-msgs:
			return m
		case <-time.After(3 * time.Second):
			t.Fatal("no message within 3 s")
		}
		return msg{}
	}
	hello := next()
	if hello.typ != "daemon_hello" || hello.models["codex"].Models[0].ID != "gpt-a" {
		t.Fatalf("hello = %+v", hello)
	}
	// A change goes out at once, carrying the new lists.
	src.update(map[string]models.Report{"codex": {Models: []models.Model{codexModel("gpt-b")}, FetchedAt: time.Now()}})
	got := next()
	if got.typ != "daemon_heartbeat" || !got.has || got.models["codex"].Models[0].ID != "gpt-b" {
		t.Fatalf("changed heartbeat = %+v", got)
	}
	// A signal with no new generation: a heartbeat goes out, without the
	// lists already sent.
	src.changed <- struct{}{}
	if hb := next(); hb.typ != "daemon_heartbeat" || hb.has {
		t.Errorf("the same lists were sent twice: %+v", hb)
	}
	select {
	case m := <-msgs:
		t.Errorf("unexpected extra message: %+v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// A probe that hangs never holds up the heartbeat: the prober runs on its
// own goroutine, and the client only reads its last snapshot.
func TestHungProbeDoesNotBlockHeartbeat(t *testing.T) {
	hung := &fakeProbe{block: make(chan struct{})}
	p := newModelProber(map[string]modelProbe{"codex": hung.probe})
	p.deadline = time.Hour
	beats := make(chan string, 64)
	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &env)
			beats <- env.Type
		}
	})
	client := NewWSClient(Config{ServerURL: ts.wsURL(), DaemonName: "d", ProtocolVersion: "1", HeartbeatInterval: 50 * time.Millisecond})
	client.SetEngineModels(p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer close(hung.block)
	go p.Run(ctx)
	go client.Run(ctx, func() []string { return nil })
	n := 0
	for n < 3 {
		select {
		case typ := <-beats:
			if typ == "daemon_heartbeat" {
				n++
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d heartbeats while a probe hung", n)
		}
	}
}
