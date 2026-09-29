package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// The two model probes shipped today (EngineSpec.ListModels). Everything they
// read is untrusted: it is parsed leniently (a malformed entry is skipped, not
// fatal), and what survives goes through models.SanitizeReported — the
// engine's own id rule and effort allowlist, capped counts and lengths,
// control/format characters stripped — before it is stored or sent.

// ─── Codex: `codex debug models` ─────────────────────────────────────────────

const (
	codexProbeTimeout   = 10 * time.Second
	codexProbeMaxOutput = 1 << 20 // today's live output is ~230 KB
)

// probeCodexModels asks the installed Codex CLI for its account-aware model
// catalog. The CLI runs with no stdin, a minimal environment (PATH and HOME,
// plus CODEX_HOME when set — no credentials of the daemon's own), in its own
// process group, killed as a group at the deadline.
func probeCodexModels(ctx context.Context) ([]models.Model, error) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return nil, errNoModelList
	}
	out, err := runProbeCommand(ctx, codexProbeTimeout, codexProbeMaxOutput, bin, "debug", "models")
	// The CLI itself exiting non-zero (logged out, a version without the
	// subcommand) is a definite "no list". Our own kill — the deadline or the
	// output cap — is not: runProbeCommand reports those as errors that are
	// not an *exec.ExitError, and the last good list is kept.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, fmt.Errorf("%w: %w", errNoModelList, err)
	}
	if err != nil {
		return nil, err
	}
	return parseCodexModels(out)
}

// probeEnv is the environment a probe CLI runs with: PATH and HOME, and the
// engines' own home-directory overrides — nothing that carries a credential.
func probeEnv() []string {
	var env []string
	for _, k := range []string{"PATH", "HOME", "CODEX_HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// errProbeOutputTooLarge: the probe wrote more than its cap.
var errProbeOutputTooLarge = errors.New("probe output larger than its cap")

// cappedBuffer keeps at most max bytes; the first write past the cap calls
// onOverflow (which kills the process) and fails.
type cappedBuffer struct {
	buf        bytes.Buffer
	max        int
	overflow   bool
	onOverflow func()
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.overflow || b.buf.Len()+len(p) > b.max {
		if !b.overflow {
			b.overflow = true
			b.onOverflow()
		}
		return 0, errProbeOutputTooLarge
	}
	return b.buf.Write(p)
}

// runProbeCommand runs bin with args and returns its stdout: no stdin
// (os/exec's /dev/null), stderr discarded, minimal env, working directory
// the user's home, its own process group killed at timeout or when stdout
// passes maxOut.
func runProbeCommand(ctx context.Context, timeout time.Duration, maxOut int, bin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // bin is a resolved engine binary (fixed engine names), args are fixed per probe; no shell, timeout and minimal probeEnv
	cmd.Env = probeEnv()
	if h := homeDir(); h != "" {
		cmd.Dir = h
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	out := &cappedBuffer{max: maxOut, onOverflow: cancel}
	cmd.Stdout = out
	err := cmd.Run()
	if out.overflow {
		return nil, fmt.Errorf("%s: %w (%d bytes)", filepath.Base(bin), errProbeOutputTooLarge, maxOut)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(bin), ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(bin), err)
	}
	return out.buf.Bytes(), nil
}

// codexLevel is one supported_reasoning_levels element, which Codex has
// spelled both as {"effort": "high", "description": …} and as a bare "high".
// Any other shape reads as "" (dropped later).
type codexLevel string

func (l *codexLevel) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*l = codexLevel(s)
		return nil
	}
	var o struct {
		Effort string `json:"effort"`
	}
	if json.Unmarshal(b, &o) == nil {
		*l = codexLevel(o.Effort)
	}
	return nil
}

type rawCodexModel struct {
	Slug                     string       `json:"slug"`
	DisplayName              string       `json:"display_name"`
	Description              string       `json:"description"`
	DefaultReasoningLevel    string       `json:"default_reasoning_level"`
	SupportedReasoningLevels []codexLevel `json:"supported_reasoning_levels"`
	Visibility               string       `json:"visibility"`
	Priority                 *float64     `json:"priority"`
}

// parseCodexModels reads `codex debug models` output: the models Codex's own
// picker lists (visibility "list"; hidden ones are internal or retired), in
// its priority order (lowest first; entries without one last, in document
// order). An entry that does not decode is skipped; a document that does not
// decode at all is an error.
func parseCodexModels(data []byte) ([]models.Model, error) {
	var doc struct {
		Models []json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("codex models: %w", err)
	}
	if doc.Models == nil {
		return nil, errors.New("codex models: no models array")
	}
	type ranked struct {
		m        rawCodexModel
		priority float64
	}
	var listed []ranked
	for _, raw := range doc.Models {
		var m rawCodexModel
		if json.Unmarshal(raw, &m) != nil || m.Visibility != "list" {
			continue
		}
		p := math.Inf(1)
		if m.Priority != nil {
			p = *m.Priority
		}
		listed = append(listed, ranked{m, p})
	}
	sort.SliceStable(listed, func(i, j int) bool { return listed[i].priority < listed[j].priority })
	out := make([]models.Model, 0, len(listed))
	for _, r := range listed {
		efforts := make([]string, 0, len(r.m.SupportedReasoningLevels))
		for _, l := range r.m.SupportedReasoningLevels {
			efforts = append(efforts, string(l))
		}
		out = append(out, models.Model{
			ID: r.m.Slug, Name: r.m.DisplayName, Description: r.m.Description,
			// Codex's own picker shows every listed model at once.
			Section: "main", Efforts: efforts, DefaultEffort: r.m.DefaultReasoningLevel,
			EffortKind: "reasoning",
		})
	}
	return models.SanitizeReported("codex", out), nil
}

// ─── Hermes: the configured endpoint's /models ───────────────────────────────

const (
	hermesProbeTimeout  = 3 * time.Second
	hermesProbeMaxBody  = 256 << 10
	hermesConfigMaxRead = 1 << 20
	// hermesMainModels is how many models lead the picker as pills; the rest
	// go under "More models".
	hermesMainModels = 4
)

// hermesModelConfig is the part of ~/.hermes/config.yaml the probe reads.
// Nothing else — in particular no api_key — is ever read into memory as a
// value, and nothing is sent with the request.
type hermesModelConfig struct {
	Provider string
	BaseURL  string
	Default  string
}

// probeHermesModels lists the models of the OpenAI-compatible endpoint Hermes
// is configured to use, when it is a `provider: custom` one — the only case
// where the list is simply that server's GET /models. Other providers (Nous
// and the like) and a missing config report no list: no guessing.
func probeHermesModels(ctx context.Context) ([]models.Model, error) {
	if _, err := exec.LookPath("hermes"); err != nil {
		return nil, errNoModelList
	}
	home := homeDir()
	if home == "" {
		return nil, errNoModelList
	}
	return probeHermesModelsAt(ctx, filepath.Join(home, ".hermes", "config.yaml"), newHermesProbeClient())
}

func probeHermesModelsAt(ctx context.Context, configPath string, client *http.Client) ([]models.Model, error) {
	cfg, err := readHermesModelConfig(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNoModelList
	}
	if err != nil {
		// Present but unreadable (not a regular file, too large, a line we
		// cannot parse): an error worth logging, not a silent "no list".
		return nil, fmt.Errorf("hermes config: %w", err)
	}
	if cfg.Provider != "custom" || cfg.BaseURL == "" {
		return nil, errNoModelList
	}
	return fetchOpenAIModels(ctx, client, cfg.BaseURL, cfg.Default)
}

// newHermesProbeClient: GET only, never follows a redirect (a 3xx is an
// error), no proxy (the endpoint is the user's own, usually on their LAN),
// and a hard timeout.
func newHermesProbeClient() *http.Client {
	tr := &http.Transport{}
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		tr = dt.Clone()
	}
	tr.Proxy = nil
	return &http.Client{
		Timeout:   hermesProbeTimeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

var yamlKeyLine = regexp.MustCompile(`^([ \t]+)([A-Za-z_][A-Za-z0-9_]*)[ \t]*:(.*)$`)

// readHermesModelConfig pulls provider, base_url and default out of the
// top-level `model:` mapping of a Hermes config.yaml with a deliberately tiny
// line parser: only those three keys, only directly under `model:`, plain or
// quoted one-line scalars. A `model:` that is a scalar (a bare model name)
// has no provider, so reports none. The file must be a regular file — it is
// opened non-blocking and checked, so a FIFO or device cannot hang the probe
// — no larger than hermesConfigMaxRead; a line over 64 KB, a flow-style
// `model:` or a value this parser cannot read exactly is an error.
func readHermesModelConfig(path string) (hermesModelConfig, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // path is the Hermes config under the user's home; opened O_NONBLOCK, must be a regular file, read capped by hermesConfigMaxRead
	if err != nil {
		return hermesModelConfig{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return hermesModelConfig{}, err
	}
	if !fi.Mode().IsRegular() {
		return hermesModelConfig{}, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > hermesConfigMaxRead {
		return hermesModelConfig{}, fmt.Errorf("%s is larger than %d bytes", path, hermesConfigMaxRead)
	}
	var cfg hermesModelConfig
	sc := bufio.NewScanner(io.LimitReader(f, hermesConfigMaxRead))
	sc.Buffer(make([]byte, 0, 64<<10), 64<<10)
	inModel := false
	indent := ""
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			// A top-level key: the model block starts or ends here.
			if inModel {
				break
			}
			if rest, ok := strings.CutPrefix(line, "model:"); ok {
				v, ok := yamlScalar(rest)
				if !ok {
					return hermesModelConfig{}, errors.New("model: is not a block mapping this probe can read")
				}
				if v != "" {
					return hermesModelConfig{}, nil
				}
				inModel = true
			}
			continue
		}
		if !inModel {
			continue
		}
		m := yamlKeyLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if indent == "" {
			indent = m[1]
		}
		if m[1] != indent {
			continue // nested deeper than model's own keys
		}
		var dst *string
		switch m[2] {
		case "provider":
			dst = &cfg.Provider
		case "base_url":
			dst = &cfg.BaseURL
		case "default":
			dst = &cfg.Default
		default:
			continue // never parsed, let alone kept: api_key and the rest
		}
		v, ok := yamlScalar(m[3])
		if !ok {
			return hermesModelConfig{}, fmt.Errorf("model.%s: not a one-line scalar this probe can read", m[2])
		}
		*dst = v
	}
	if err := sc.Err(); err != nil {
		return hermesModelConfig{}, err
	}
	return cfg, nil
}

// yamlScalar reads a plain or quoted one-line scalar, dropping a trailing
// comment (a # after a space or tab). ok is false for anything it cannot
// read exactly: a flow collection, block scalar, anchor, alias or tag; an
// unterminated quote; text after a closing quote other than a comment; a
// double-quoted escape other than \" \\ and \/.
func yamlScalar(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s[0] == '#' {
		return "", true
	}
	switch s[0] {
	case '"':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			switch c := s[i]; c {
			case '\\':
				if i+1 >= len(s) {
					return "", false
				}
				i++
				switch s[i] {
				case '"', '\\', '/':
					b.WriteByte(s[i])
				default:
					return "", false
				}
			case '"':
				return b.String(), onlyComment(s[i+1:])
			default:
				b.WriteByte(c)
			}
		}
		return "", false
	case '\'':
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] != '\'' {
				b.WriteByte(s[i])
				continue
			}
			if i+1 < len(s) && s[i+1] == '\'' { // '' is an escaped quote
				b.WriteByte('\'')
				i++
				continue
			}
			return b.String(), onlyComment(s[i+1:])
		}
		return "", false
	case '[', '{', '|', '>', '&', '*', '!':
		return "", false
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			s = s[:i]
			break
		}
	}
	return strings.TrimSpace(s), true
}

// onlyComment reports whether rest (what follows a closing quote) is empty or
// a comment.
func onlyComment(rest string) bool {
	rest = strings.TrimLeft(rest, " \t")
	return rest == "" || rest[0] == '#'
}

// fetchOpenAIModels GETs <baseURL>/models from an OpenAI-compatible server
// and returns its model ids as Hermes picker entries: preferred (the
// configured default) first when listed, then the server's order. Only
// http/https URLs without embedded credentials are fetched; no auth is sent;
// 401/403 means "no list" (errNoModelList); a redirect, another status, an
// oversized or non-JSON body is an error.
func fetchOpenAIModels(ctx context.Context, client *http.Client, baseURL, preferred string) ([]models.Model, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errNoModelList
	}
	if u.User != nil {
		// Credentials in the URL would be sent; this probe sends none.
		return nil, errNoModelList
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	ctx, cancel := context.WithTimeout(ctx, hermesProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hermes models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errNoModelList
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, fmt.Errorf("hermes models: HTTP %d redirect not followed", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("hermes models: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, hermesProbeMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("hermes models: %w", err)
	}
	if len(body) > hermesProbeMaxBody {
		return nil, fmt.Errorf("hermes models: body larger than %d bytes", hermesProbeMaxBody)
	}
	return parseOpenAIModels(body, preferred)
}

// parseOpenAIModels reads an OpenAI-compatible /models body
// ({"data":[{"id":…, "max_model_len":…}]}). Every model is offered every
// Hermes reasoning level with no default: Hermes does not report per-model
// support, and no default means "don't pass --reasoning".
func parseOpenAIModels(data []byte, preferred string) ([]models.Model, error) {
	var doc struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("hermes models: %w", err)
	}
	if doc.Data == nil {
		return nil, errors.New("hermes models: no data array")
	}
	efforts := models.EffortsFor("hermes")
	// One pass, linear: duplicates and ids the engine would refuse are
	// skipped as they come, entries past the cap are not kept (though the
	// scan goes on to find the preferred one), and the preferred model's
	// first occurrence is moved to the front once at the end.
	var out []models.Model
	seen := map[string]bool{}
	preferredAt := -1
	for _, raw := range doc.Data {
		var m struct {
			ID          string          `json:"id"`
			MaxModelLen json.RawMessage `json:"max_model_len"` // optional, vLLM's; any type
		}
		if json.Unmarshal(raw, &m) != nil || m.ID == "" || seen[m.ID] || !models.ValidModelFor("hermes", m.ID) {
			continue
		}
		isPreferred := preferred != "" && m.ID == preferred
		if len(out) >= models.MaxReportedModels && !isPreferred {
			continue
		}
		seen[m.ID] = true
		entry := models.Model{ID: m.ID, Name: m.ID, Efforts: efforts, EffortKind: "reasoning"}
		var ctxLen float64
		if json.Unmarshal(m.MaxModelLen, &ctxLen) == nil && ctxLen >= 1 && ctxLen < 1e9 {
			entry.Description = fmt.Sprintf("%d-token context", int64(ctxLen))
		}
		if isPreferred {
			preferredAt = len(out)
		}
		out = append(out, entry)
	}
	if preferredAt > 0 {
		p := out[preferredAt]
		copy(out[1:preferredAt+1], out[:preferredAt])
		out[0] = p
	}
	clean := models.SanitizeReported("hermes", out)
	for i := range clean {
		if i < hermesMainModels {
			clean[i].Section = "main"
		}
	}
	return clean, nil
}
