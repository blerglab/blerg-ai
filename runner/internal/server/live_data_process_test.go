package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/contracts/livemcp"
	"github.com/blerglab/blerg-ai/contracts/livetls"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type liveProcess struct {
	t      *testing.T
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *json.Decoder
	stderr bytes.Buffer
	joined bool
	mu     sync.Mutex
}

func startLiveProcess(t *testing.T, binary, name, path string, listener net.Listener, market bool) *liveProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	p := &liveProcess{t: t}
	p.cmd = exec.CommandContext(ctx, binary, "-test.run=^"+name+"$")
	p.cmd.Env = append(os.Environ(), "BLERG_LIVE_PROCESS_CONFIG="+path)
	if market {
		p.cmd.Env = append(p.cmd.Env, "BLERG_LIVE_PROCESS_MARKET=1")
	}
	tcp, ok := listener.(*net.TCPListener)
	if !ok {
		t.Fatal("fixture listener type")
	}
	file, e := tcp.File()
	if e != nil {
		t.Fatal("fixture inherited listener")
	}
	p.cmd.ExtraFiles = []*os.File{file}
	p.input, e = p.cmd.StdinPipe()
	if e != nil {
		t.Fatal("fixture stdin")
	}
	output, e := p.cmd.StdoutPipe()
	if e != nil {
		t.Fatal("fixture stdout")
	}
	p.output = json.NewDecoder(output)
	p.cmd.Stderr = &p.stderr
	if p.cmd.Start() != nil {
		t.Fatal("start service process")
	}
	_ = file.Close()
	_ = listener.Close()
	t.Cleanup(func() {
		p.stop()
		cancel()
		for _, canary := range []string{"process-provider-canary", "process-internal-canary"} {
			if strings.Contains(p.stderr.String(), canary) {
				t.Error("secret leaked to service logs")
			}
		}
	})
	return p
}
func (p *liveProcess) stop() {
	if p.joined {
		return
	}
	_ = p.input.Close()
	if p.cmd.Wait() != nil {
		p.t.Error("service fixture exited unsuccessfully")
	}
	p.joined = true
}
func (p *liveProcess) crash() {
	p.t.Helper()
	if p.joined || p.cmd.Process.Kill() != nil {
		p.t.Fatal("fixture crash failed")
	}
	_ = p.input.Close()
	if p.cmd.Wait() == nil {
		p.t.Fatal("fixture crash did not interrupt process")
	}
	p.joined = true
}
func (p *liveProcess) read() liveProcessReply {
	p.t.Helper()
	var r liveProcessReply
	if p.output.Decode(&r) != nil {
		p.t.Fatal("service process response unavailable")
	}
	return r
}
func (p *liveProcess) call(c liveProcessCommand) liveProcessReply {
	p.t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if json.NewEncoder(p.input).Encode(c) != nil {
		p.t.Fatal("service process request unavailable")
	}
	return p.read()
}

type liveProcessMatrix struct {
	t                    *testing.T
	config               liveProcessConfig
	path, coreBinary     string
	core, runner, market *liveProcess
	ready                liveProcessReply
	admin                *pgxpool.Pool
	revision             int64
	processes            []*liveProcess
	secrets              []string
}

func newLiveProcessMatrix(t *testing.T) *liveProcessMatrix {
	t.Helper()
	binary := os.Getenv("BLERG_LIVE_PROCESS_CORE_BINARY")
	// Record the external helper's version in Go's test-cache inputs. A Core-only
	// guard change must not reuse a cached Runner process proof.
	version := os.Getenv("BLERG_LIVE_PROCESS_CORE_VERSION")
	if binary != "" && len(version) != 64 {
		t.Fatal("Core fixture digest missing; use scripts/test-live-data-process.sh")
	}

	if binary == "" {
		t.Skip("requires separately compiled Core process fixture")
	}
	admin, e := pgxpool.New(t.Context(), os.Getenv("TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal("fixture admin database")
	}
	t.Cleanup(admin.Close)
	root := t.TempDir()
	config := liveProcessConfig{Root: root, CoreDB: "proof_core_" + strings.ReplaceAll(newUUID(), "-", ""), RunnerDB: "proof_runner_" + strings.ReplaceAll(newUUID(), "-", "")}
	for _, database := range []string{config.CoreDB, config.RunnerDB} {
		if _, e = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()+" TEMPLATE template0"); e != nil {
			t.Fatal("create owned fixture database")
		}
		t.Cleanup(func() {
			if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)"); err != nil {
				t.Error("drop owned fixture database")
			}
		})
	}
	if os.Mkdir(filepath.Join(root, "authority"), 0700) != nil {
		t.Fatal("private authority directory")
	}
	if livetls.Provision(filepath.Join(root, "tls")) != nil {
		t.Fatal("fixture transport provisioning")
	}
	listeners := map[string]net.Listener{}
	for _, name := range []string{"core", "runner", "market"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal("fixture listener")
		}
		listeners[name] = listener
		t.Cleanup(func() { _ = listener.Close() })
	}
	config.CoreAddress = listeners["core"].Addr().String()
	config.RunnerAddress = listeners["runner"].Addr().String()
	config.MarketURL = "http://" + listeners["market"].Addr().String() + "/mcp"
	path := filepath.Join(root, "private.json")
	raw, _ := json.Marshal(config)
	if os.WriteFile(path, raw, 0600) != nil {
		t.Fatal("private fixture config")
	}
	h := &liveProcessMatrix{t: t, config: config, path: path, admin: admin, coreBinary: binary}
	h.market = startLiveProcess(t, os.Args[0], "TestLiveDataMarketProcessFixture", path, listeners["market"], true)
	if !h.market.read().Ready {
		t.Fatal("market process readiness")
	}
	h.core = startLiveProcess(t, binary, "TestLiveDataCoreProcessFixture", path, listeners["core"], false)
	if ready := h.core.read(); !ready.Ready || !ready.Available {
		t.Fatal("Core process readiness or authority")
	}
	h.runner = startLiveProcess(t, os.Args[0], "TestLiveDataRunnerProcessFixture", path, listeners["runner"], false)
	h.ready = h.runner.read()
	if !h.ready.Ready {
		t.Fatal("Runner process readiness")
	}
	h.processes = []*liveProcess{h.market, h.core, h.runner}
	for _, token := range h.ready.Tokens {
		h.secrets = append(h.secrets, token)
	}
	t.Cleanup(func() {
		for i := len(h.processes) - 1; i >= 0; i-- {
			h.processes[i].stop()
		}
		for _, p := range h.processes {
			for _, secret := range h.secrets {
				if secret != "" && strings.Contains(p.stderr.String(), secret) {
					t.Error("signed credential leaked to process logs")
				}
			}
		}
	})
	return h
}
func (h *liveProcessMatrix) prepare() ld.Revision {
	h.t.Helper()
	h.revision++
	identity, e := livetls.Load(filepath.Join(h.config.Root, "tls", "runner"), "runner")
	if e != nil {
		h.t.Fatal("fixture identity")
	}
	definition, hash := mcpgw.CanonicalTool("quotes", "Simulated market quotes", json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"string"}},"required":["symbol"]}`))
	r := ld.Revision{Scope: ld.Scope{Installation: identity.Installation, Runner: identity.Runner, Owner: mcpFixtureOwner, Binding: newUUID(), Tool: "quotes", Hash: hash, Revision: h.revision}, ToolDefinition: definition, Arguments: json.RawMessage(`{"symbol":"NVDA"}`), Mapping: livemcp.Mapping{RowsPath: "/quotes", Fields: []livemcp.Field{{Name: "value", Path: "/close", Type: "number"}}, Keys: []string{"value"}}, IntervalSeconds: 15, ExpiresAt: time.Now().UTC().Add(time.Hour), OwnerOnly: true}
	reply := h.core.call(liveProcessCommand{Action: "prepare", Revision: r})
	if reply.Error != "" {
		h.t.Fatal("Core fixture approval refused")
	}
	r = reply.Revision
	if reply = h.runner.call(liveProcessCommand{Action: "activate", Revision: r}); reply.Error != "" {
		h.t.Fatal("Runner fixture activation refused")
	}
	h.market.call(liveProcessCommand{Action: "mode", Mode: "good"})
	return r
}
func (h *liveProcessMatrix) demand() {
	h.t.Helper()
	if h.runner.call(liveProcessCommand{Action: "demand"}).Error != "" {
		h.t.Fatal("owner demand refused")
	}
}
func (h *liveProcessMatrix) attempt() liveProcessReply {
	h.t.Helper()
	h.demand()
	h.runner.call(liveProcessCommand{Action: "due"})
	r := h.runner.call(liveProcessCommand{Action: "attempt"})
	if r.Error != "" || r.Attempt == "" || r.Token == "" {
		h.t.Fatal("committed attempt or fresh mint unavailable")
	}
	h.secrets = append(h.secrets, r.Token)
	return r
}
func (h *liveProcessMatrix) execute(a liveProcessReply) liveProcessReply {
	h.t.Helper()
	h.runner.call(liveProcessCommand{Action: "start", Token: a.Token, Attempt: a.Attempt})
	return h.runner.call(liveProcessCommand{Action: "finish"})
}
func (h *liveProcessMatrix) restartCore() liveProcessReply {
	h.t.Helper()
	h.core.stop()
	listener, e := net.Listen("tcp", h.config.CoreAddress)
	if e != nil {
		h.t.Fatal("restart Core listener")
	}
	h.core = startLiveProcess(h.t, h.coreBinary, "TestLiveDataCoreProcessFixture", h.path, listener, false)
	h.processes = append(h.processes, h.core)
	return h.core.read()
}
func (h *liveProcessMatrix) restartRunner(r ld.Revision) {
	h.t.Helper()
	h.runner.stop()
	listener, e := net.Listen("tcp", h.config.RunnerAddress)
	if e != nil {
		h.t.Fatal("restart Runner listener")
	}
	h.runner = startLiveProcess(h.t, os.Args[0], "TestLiveDataRunnerProcessFixture", h.path, listener, false)
	h.processes = append(h.processes, h.runner)
	h.ready = h.runner.read()
	for _, token := range h.ready.Tokens {
		h.secrets = append(h.secrets, token)
	}
	loaded := h.runner.call(liveProcessCommand{Action: "load existing", Revision: r})
	if !h.ready.Ready || loaded.Error != "" {
		h.t.Fatal("Runner state recovery")
	}
	h.ready.Workspace = loaded.Workspace
}
func (h *liveProcessMatrix) cloneCoreSnapshot() string {
	h.t.Helper()
	h.core.stop()
	name := "proof_backup_" + strings.ReplaceAll(newUUID(), "-", "")
	if _, e := h.admin.Exec(h.t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{h.config.CoreDB}.Sanitize()); e != nil {
		h.t.Fatal("clone owned Core snapshot")
	}
	h.t.Cleanup(func() {
		if _, e := h.admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
			h.t.Error("drop owned snapshot")
		}
	})
	return name
}
func TestLiveDataCrossProcessGateway(t *testing.T) {
	h := newLiveProcessMatrix(t)
	t.Run("real issuer receipts and approved mapping", func(t *testing.T) {
		h.prepare()
		h.demand()
		h.runner.call(liveProcessCommand{Action: "tick"})
		result := h.runner.call(liveProcessCommand{Action: "finish"})
		sample := h.runner.call(liveProcessCommand{Action: "sample"})
		counts := h.market.call(liveProcessCommand{Action: "counts"})
		audit := h.core.call(liveProcessCommand{Action: "audit counts"})
		if result.Cause != "" || sample.Cause != "" || !bytes.Contains(sample.Rows, []byte("185.25")) || counts.Calls != 1 || audit.SecretAudits != 2 {
			t.Fatal("production poll did not traverse issuer, receipts, provider and mapping")
		}
	})
	t.Run("credential receipt replay", func(t *testing.T) {
		h.prepare()
		a := h.attempt()
		before := h.core.call(liveProcessCommand{Action: "audit counts"})
		if h.runner.call(liveProcessCommand{Action: "receipt", Token: a.Token, Attempt: a.Attempt, Mode: "list"}).Error != "" {
			t.Fatal("initial committed receipt refused")
		}
		if h.runner.call(liveProcessCommand{Action: "receipt", Token: a.Token, Attempt: a.Attempt, Mode: "list"}).Error == "" {
			t.Fatal("credential receipt replay accepted")
		}
		after := h.core.call(liveProcessCommand{Action: "audit counts"})
		if after.SecretAudits != before.SecretAudits+1 {
			t.Fatal("replayed receipt released another credential")
		}
	})
	t.Run("revocation during real provider call", func(t *testing.T) {
		r := h.prepare()
		a := h.attempt()
		h.market.call(liveProcessCommand{Action: "mode", Mode: "hold"})
		h.runner.call(liveProcessCommand{Action: "start", Token: a.Token, Attempt: a.Attempt})
		if h.market.call(liveProcessCommand{Action: "await call"}).Error != "" {
			t.Fatal("provider call never started")
		}
		if h.core.call(liveProcessCommand{Action: "revoke", Revision: r}).Error != "" {
			t.Fatal("durable mid-call revocation failed")
		}
		h.market.call(liveProcessCommand{Action: "release"})
		result := h.runner.call(liveProcessCommand{Action: "finish"})
		if result.Cause != "authority_revoked" || len(result.Rows) != 0 && string(result.Rows) != "null" {
			t.Fatalf("mid-call outcome cause=%s has_rows=%t", result.Cause, len(result.Rows) > 0 && string(result.Rows) != "null")
		}
	})
	t.Run("owner other account and administrator socket delivery", func(t *testing.T) {
		r := h.prepare()
		h.demand()
		h.runner.call(liveProcessCommand{Action: "tick"})
		h.runner.call(liveProcessCommand{Action: "finish"})
		for _, person := range []string{"owner", "other", "admin"} {
			authority := h.runner.call(liveProcessCommand{Action: "scope", Mode: person})
			if authority.Available != (person == "owner") {
				t.Fatalf("authority barrier principal=%s expected_owner=%t", person, person == "owner")
			}
			queued := h.runner.call(liveProcessCommand{Action: "queued", Mode: person})
			var frame protocol.DatasetDelta
			if !queued.Available || json.Unmarshal(queued.Rows, &frame) != nil {
				t.Fatal("queued delivery fixture")
			}
			if (len(frame.Upserts) > 0) != (person == "owner") {
				t.Fatal("queued private frame crossed owner boundary")
			}

			dialer := websocket.Dialer{Subprotocols: []string{"bearer", h.ready.Tokens[person]}}
			socket, _, e := dialer.Dial("ws"+strings.TrimPrefix(h.ready.BrowserURL, "http")+"/ws/browser", nil)
			if e != nil {
				t.Fatal("authenticated process socket")
			}
			t.Cleanup(func() { _ = socket.Close() })
			q := protocol.DatasetSubscribe{Type: "subscribe_dataset", SessionID: h.ready.Workspace, SubscriptionID: newUUID(), Data: protocol.DatasetReference{Kind: "dataset", Name: "blerg.mcp", Parameters: map[string]string{"binding": r.Scope.Binding}}}
			if socket.WriteJSON(q) != nil {
				t.Fatal("socket subscribe")
			}
			_ = socket.SetReadDeadline(time.Now().Add(10 * time.Second))
			for {
				var delta protocol.DatasetDelta
				if socket.ReadJSON(&delta) != nil {
					t.Fatal("socket sample unavailable")
				}
				if delta.Type != "dataset_delta" {
					continue
				}
				if person == "owner" {
					if len(delta.Upserts) != 1 || delta.Upserts[0]["value"] != 185.25 {
						t.Fatal("owner's process sample missing")
					}
				} else if len(delta.Upserts) != 0 {
					t.Fatal("private data delivered to another account or administrator")
				}
				break
			}
			_ = socket.Close()
		}
	})
	t.Run("checkpointed database restore refuses authority until operator fence", func(t *testing.T) {
		h.t = t
		r := h.prepare()
		a := h.attempt()
		snapshot := h.cloneCoreSnapshot()
		if ready := h.restartCore(); !ready.Ready || !ready.Available {
			t.Fatal("ordinary Core restart discarded authority")
		}
		if result := h.execute(a); result.Cause != "authority_revoked" {
			t.Fatal("restart retained prior approval", result.Cause)
		}
		fresh := h.prepare()
		if result := h.execute(h.attempt()); result.Cause != "" {
			t.Fatal("reapproval after restart could not poll", result.Cause)
		}
		if h.core.call(liveProcessCommand{Action: "revoke", Revision: fresh}).Error != "" {
			t.Fatal("snapshot-era grant revocation failed")
		}
		h.core.stop()
		if _, e := h.admin.Exec(t.Context(), "DROP DATABASE "+pgx.Identifier{h.config.CoreDB}.Sanitize()+" WITH (FORCE)"); e != nil {
			t.Fatal("replace owned fixture database")
		}
		if _, e := h.admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{h.config.CoreDB}.Sanitize()+" TEMPLATE "+pgx.Identifier{snapshot}.Sanitize()); e != nil {
			t.Fatal("restore owned fixture snapshot")
		}
		if ready := h.restartCore(); !ready.Ready || ready.Available {
			t.Fatal("database restore resurrected revoked authority")
		}
		if h.runner.call(liveProcessCommand{Action: "mint"}).Error == "" {
			t.Fatal("restored grant minted before operator fence")
		}
		if h.core.call(liveProcessCommand{Action: "fence"}).Error != "" {
			t.Fatal("explicit operator fence failed")
		}
		if ready := h.restartCore(); !ready.Ready || !ready.Available {
			t.Fatal("fenced Core recovery failed")
		}
		if h.runner.call(liveProcessCommand{Action: "mint"}).Error == "" {
			t.Fatal("operator fence reactivated restored approval")
		}
		next := h.prepare()
		if next.Scope.AuthorityEpoch <= r.Scope.AuthorityEpoch {
			t.Fatal("operator fence did not advance external epoch")
		}
		if result := h.execute(h.attempt()); result.Cause != "" || !bytes.Contains(result.Rows, []byte("185.25")) {
			t.Fatal("new explicit approval after fence could not poll")
		}
	})

}

func TestLiveDataCrossProcessToolFences(t *testing.T) {
	h := newLiveProcessMatrix(t)
	for _, mode := range []string{"missing tool", "duplicate tool", "not read only", "changed hash"} {
		t.Run(mode, func(t *testing.T) {
			h.t = t
			r := h.prepare()
			a := h.attempt()
			if h.core.call(liveProcessCommand{Action: "auto accept", Revision: r}).Error != "" {
				t.Fatal("fixture pin policy")
			}
			before := h.market.call(liveProcessCommand{Action: "counts"})
			h.market.call(liveProcessCommand{Action: "mode", Mode: mode})
			result := h.execute(a)
			if result.Cause != "tool_changed" || len(result.Rows) > 0 && string(result.Rows) != "null" {
				t.Fatalf("tool authority not fenced: cause=%s", result.Cause)
			}
			after := h.market.call(liveProcessCommand{Action: "counts"})
			if after.Lists != before.Lists+1 || after.Calls != before.Calls {
				t.Fatal("fresh listing skipped or changed tool called")
			}
			h.market.call(liveProcessCommand{Action: "mode", Mode: "good"})
			if h.runner.call(liveProcessCommand{Action: "mint"}).Error == "" {
				t.Fatal("auto_accept resurrected tool authority without a new binding approval")
			}
		})
	}
}

func TestLiveDataCrossProcessFailedRevocationFencesLocally(t *testing.T) {
	h := newLiveProcessMatrix(t)
	r := h.prepare()
	a := h.attempt()
	if h.core.call(liveProcessCommand{Action: "block revocation"}).Error != "" {
		t.Fatal("fixture refusal setup")
	}
	h.market.call(liveProcessCommand{Action: "mode", Mode: "changed hash"})
	result := h.execute(a)
	if result.Cause == "" || len(result.Rows) > 0 && string(result.Rows) != "null" {
		t.Fatal("known changed tool returned rows")
	}
	state := h.runner.call(liveProcessCommand{Action: "sample"})
	if state.Enabled || state.Retained || state.Cause != "tool_changed" {
		t.Fatal("failed Core revocation left the known unsafe revision locally enabled")
	}
	if h.core.call(liveProcessCommand{Action: "unblock revocation"}).Error != "" {
		t.Fatal("fixture refusal cleanup")
	}
	h.market.call(liveProcessCommand{Action: "mode", Mode: "good"})
	h.runner.call(liveProcessCommand{Action: "activate", Revision: r})
	if h.runner.call(liveProcessCommand{Action: "sample"}).Enabled {
		t.Fatal("old receipt reactivated locally fenced revision")
	}
	r.Scope.Revision++
	r.Scope.Digest = ""
	next := h.core.call(liveProcessCommand{Action: "prepare", Revision: r})
	if next.Error != "" {
		t.Fatal("new fixture approval")
	}
	if h.runner.call(liveProcessCommand{Action: "activate", Revision: next.Revision}).Error != "" {
		t.Fatal("new approval activation")
	}
	if result = h.execute(h.attempt()); result.Cause != "" || !bytes.Contains(result.Rows, []byte("185.25")) {
		t.Fatal("fresh approval could not recover local fence")
	}
}

func TestLiveDataCrossProcessProviderFailures(t *testing.T) {
	h := newLiveProcessMatrix(t)
	for _, tc := range []struct {
		mode, cause string
		enabled     bool
		delay       float64
	}{
		{"outage", "provider_unavailable", true, 14},
		{"rate limited", "provider_rate_limited", true, 89},
		{"listing outage", "tool_listing_unavailable", true, 14},
		{"auth refused", "connection_needs_sign_in", false, -1},
		{"missing value", "mapping_failed", false, 14},
		{"null value", "mapping_failed", false, 14},
		{"too many rows", "mapping_failed", false, 14},
		{"oversized", "invalid_result", true, 14},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			h.t = t
			h.prepare()
			h.demand()
			h.runner.call(liveProcessCommand{Action: "tick"})
			h.runner.call(liveProcessCommand{Action: "finish"})
			before := h.runner.call(liveProcessCommand{Action: "sample"})
			if !bytes.Contains(before.Rows, []byte("185.25")) || before.PayloadAt == "" {
				t.Fatal("initial authorized sample missing")
			}
			h.runner.call(liveProcessCommand{Action: "advance cadence"})
			h.market.call(liveProcessCommand{Action: "mode", Mode: tc.mode})
			h.runner.call(liveProcessCommand{Action: "tick"})
			h.runner.call(liveProcessCommand{Action: "finish"})
			after := h.runner.call(liveProcessCommand{Action: "sample"})
			if after.Cause != tc.cause || after.Enabled != tc.enabled || (tc.mode != "auth refused" && (!bytes.Equal(before.Rows, after.Rows) || before.PayloadAt != after.PayloadAt)) || (tc.mode == "auth refused" && after.Retained) {
				t.Fatalf("provider failure replaced good data or state: cause=%s enabled=%t", after.Cause, after.Enabled)
			}
			if after.RetrySeconds < tc.delay {
				t.Fatalf("provider minimum retry delay ignored: %.1f", after.RetrySeconds)
			}
			calls := h.market.call(liveProcessCommand{Action: "counts"}).Calls
			h.runner.call(liveProcessCommand{Action: "tick"})
			h.runner.call(liveProcessCommand{Action: "finish"})
			if h.market.call(liveProcessCommand{Action: "counts"}).Calls != calls {
				t.Fatal("failure scheduled an overlapping or early retry")
			}
		})
	}
}
