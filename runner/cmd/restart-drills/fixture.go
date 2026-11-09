package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

var dataDir = "/data"

type privateState struct{ Password, Token, DesktopID string }
type turnState struct {
	Case, Session, Path, Base, Engine, Native, OriginalEngine string
	Released                                                  bool
	Prefix                                                    json.RawMessage
}
type daemonState struct {
	PID  int
	Boot string
}
type fixture struct {
	mu            sync.Mutex
	barrierNotice chan struct{}
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path) //nolint:gosec // Paths are private fixture state or an operator-selected evidence directory.
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
func dbURL(db string) (string, error) {
	u, err := url.Parse(os.Getenv("DRILL_ADMIN_DATABASE_URL"))
	if err != nil || u.Host != "restart-postgres:5432" || u.Scheme != "postgres" || u.User == nil || u.User.Username() != "blerg" {
		return "", errors.New("only generated namespace-local database accepted")
	}
	u.Path = "/" + db
	return u.String(), nil
}
func connectDB(ctx context.Context, db string) (*pgx.Conn, error) {
	dsn, err := dbURL(db)
	if err != nil {
		return nil, err
	}
	return pgx.Connect(ctx, dsn)
}
func until(ctx context.Context, check func() (bool, error)) error {
	timer := time.NewTicker(200 * time.Millisecond)
	defer timer.Stop()
	for {
		ok, err := check()
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("fixture deadline exceeded")
		case <-timer.C:
		}
	}
}
func initDB(ctx context.Context) error {
	var conn *pgx.Conn
	if err := until(ctx, func() (bool, error) { var err error; conn, err = connectDB(ctx, "postgres"); return err == nil, nil }); err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(433)"); err != nil {
		return err
	}
	for _, db := range []string{"blerg_core", "blerg_board", "blerg_runner"} {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)", db).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{db}.Sanitize()); err != nil {
				return err
			}
		}
	}
	return nil
}
func apiCall(ctx context.Context, client *http.Client, method, base, path, token, internal string, body any, out any) error {
	var input io.Reader
	if body != nil {
		input = bytes.NewReader(encode(body))
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, input)
	if err != nil {
		return errors.New("invalid fixture request")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if internal != "" {
		req.Header.Set("X-Internal-Key", internal)
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("fixture transport failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("fixture request refused (%d)", resp.StatusCode)
	}
	if out != nil {
		if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out); err != nil {
			return errors.New("invalid fixture response")
		}
	}
	return nil
}

var fixtureTransport = http.DefaultTransport
var fixtureControlURL = "http://127.0.0.1:8099"

func safeClient() *http.Client {
	return &http.Client{Transport: fixtureTransport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func uuid() string {
	s := random()
	return s[:8] + "-" + s[8:12] + "-4" + s[13:16] + "-8" + s[17:20] + "-" + s[20:32]
}
func bootstrap(ctx context.Context) error {
	var p privateState
	if err := readJSON(dataDir+"/private.json", &p); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		p = privateState{Password: random(), DesktopID: uuid()}
		if err = save(dataDir, "private.json", p); err != nil {
			return err
		}
	}
	if p.Token != "" {
		return nil
	}
	conn, err := connectDB(ctx, "blerg_core")
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	var count int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM accounts").Scan(&count); err != nil {
		return err
	}
	jar, _ := cookiejar.New(nil)
	client := safeClient()
	client.Jar = jar
	if count == 0 {
		code := random()
		sum := sha256.Sum256([]byte(code))
		if _, err = conn.Exec(ctx, "INSERT INTO setup_code(id,code_hash,expires_at) VALUES(true,$1,now()+interval '1 hour') ON CONFLICT(id) DO UPDATE SET code_hash=EXCLUDED.code_hash,expires_at=EXCLUDED.expires_at", hex.EncodeToString(sum[:])); err != nil {
			return err
		}
		err = apiCall(ctx, client, "POST", "http://blerg-core:8080", "/auth/setup", "", "", object{"code": code, "provider_subject": "restart-fixture", "password": p.Password}, nil)
		if err != nil {
			return err
		}
	} else {
		form := url.Values{"provider_subject": {"restart-fixture"}, "password": {p.Password}}
		req, e := http.NewRequestWithContext(ctx, "POST", "http://blerg-core:8080/auth/login", strings.NewReader(form.Encode()))
		if e != nil {
			return e
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, e := client.Do(req)
		if e != nil {
			return errors.New("fixture login unavailable")
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			return errors.New("fixture login refused")
		}
	}
	//nolint:gosec // Destination is the fixed namespace-local Core service; return_to is escaped data, never followed.
	req, err := http.NewRequestWithContext(ctx, "GET", "http://blerg-core:8080/auth/refresh?return_to="+url.QueryEscape(os.Getenv("DRILL_CORE_ORIGIN")+"/app"), nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req) //nolint:gosec // Fixed Core destination; redirects are disabled.
	if err != nil {
		return errors.New("fixture identity refresh failed")
	}
	_ = resp.Body.Close()
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != 302 {
		return errors.New("fixture identity refresh refused")
	}
	fragment, _ := url.ParseQuery(location.Fragment)
	human := fragment.Get("access_token")
	if human == "" {
		return errors.New("fixture identity missing")
	}
	var issued struct{ Token string }
	err = apiCall(ctx, client, "POST", "http://blerg-core:8080", "/api/tokens", human, "", object{"name": "restart-drills", "preset": "run-sessions", "expires_in_days": 1}, &issued)
	if err != nil {
		return err
	}
	p.Token = issued.Token
	var desktop struct{ Token string }
	err = apiCall(ctx, client, "POST", "http://blerg-core:8080", "/internal/desktop-tokens/issue", "", os.Getenv("BLERG_CORE_INTERNAL_KEY"), object{"desktop_id": p.DesktopID, "ownership_revision": 1}, &desktop)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dataDir+"/state", 0700); err != nil {
		return err
	}
	if err = save(dataDir+"/state", "desktop-credential.json", object{"desktop_id": p.DesktopID, "token": desktop.Token}); err != nil {
		return err
	}
	return save(dataDir, "private.json", p)
}
func runnerCall(ctx context.Context, method, path string, body, out any) error {
	var p privateState
	if err := readJSON(dataDir+"/private.json", &p); err != nil {
		return err
	}
	err := apiCall(ctx, safeClient(), method, "http://blerg-runner:8080", path, p.Token, "", body, out)
	return err
}
func connected(ctx context.Context) (bool, error) {
	var p privateState
	if err := readJSON(dataDir+"/private.json", &p); err != nil {
		return false, err
	}
	conn, err := connectDB(ctx, "blerg_runner")
	if err != nil {
		return false, nil
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = conn.Close(closeCtx)
	}()
	var ok bool
	err = conn.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM daemons WHERE id=$1 AND status='connected')", p.DesktopID).Scan(&ok)
	return ok, err
}
func (f *fixture) begin(ctx context.Context, name string) error {
	if name != "daemon" && name != "server" && name != "upgrade" {
		return errors.New("unknown case")
	}
	if err := bootstrap(ctx); err != nil {
		return err
	}
	if err := until(ctx, func() (bool, error) { return connected(ctx) }); err != nil {
		return err
	}
	if err := save(dataDir, "active.json", turnState{Case: name}); err != nil {
		return err
	}
	var p privateState
	if err := readJSON(dataDir+"/private.json", &p); err != nil {
		return err
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	// Contract no_repo allocates a fresh namespace-local scratch Git repository.
	err := runnerCall(ctx, "POST", "/api/runner/start", object{"runtime": "daemon", "daemon_id": p.DesktopID, "no_repo": true, "engine": "claude", "title": "Restart drill " + name, "prompt": "restart-drill " + name, "interaction": "unattended"}, &started)
	if err != nil {
		return err
	}
	if started.SessionID == "" {
		return errors.New("missing owned session")
	}
	f.mu.Lock()
	var a turnState
	err = readJSON(dataDir+"/active.json", &a)
	if err == nil {
		a.Session = started.SessionID
		err = save(dataDir, "active.json", a)
	}
	f.mu.Unlock()
	return err
}
func (f *fixture) barrier(ctx context.Context, a turnState) error {
	if !strings.HasPrefix(a.Path, dataDir+"/repos/") || strings.Contains(a.Path, "..") || a.Base == "" {
		return errors.New("invalid fixture workspace")
	}
	if err := until(ctx, func() (bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var active turnState
		if err := readJSON(dataDir+"/active.json", &active); err != nil {
			return false, err
		}
		if active.Session == "" {
			return false, nil
		}
		if active.Session != a.Session {
			return false, errors.New("unowned session barrier")
		}
		return true, nil
	}); err != nil {
		return err
	}
	f.mu.Lock()
	var active turnState
	err := readJSON(dataDir+"/active.json", &active)
	if err == nil {
		if active.Path != "" && (active.Path != a.Path || active.Base != a.Base || active.Native != a.Native) {
			err = errors.New("workspace identity changed")
		} else {
			active.Path = a.Path
			active.Base = a.Base
			active.Engine = a.Engine
			active.Native = a.Native
			err = save(dataDir, "active.json", active)
		}
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if f.barrierNotice != nil {
		f.barrierNotice <- struct{}{}
	}
	return until(ctx, func() (bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var active turnState
		if err := readJSON(dataDir+"/active.json", &active); err != nil {
			return false, err
		}
		return active.Released, nil
	})
}
func snapshot(ctx context.Context, a turnState) (json.RawMessage, error) {
	var raw json.RawMessage
	err := runnerCall(ctx, "GET", "/api/runner/sessions/"+a.Session+"/events?after_seq=0&limit=400", nil, &raw)
	return raw, err
}
func eventProof(before, after json.RawMessage) error {
	type event struct {
		Seq     int64           `json:"seq"`
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}
	var b, a struct {
		Events  []event `json:"events"`
		HasMore bool    `json:"has_more"`
	}
	if json.Unmarshal(before, &b) != nil || json.Unmarshal(after, &a) != nil || a.HasMore || b.HasMore || len(b.Events) == 0 || len(a.Events) < len(b.Events) {
		return errors.New("incomplete transcript receipt")
	}
	for i, e := range b.Events {
		if !bytes.Equal(encode(e), encode(a.Events[i])) {
			return errors.New("durable event prefix changed")
		}
	}
	replies, done := 0, 0
	for _, e := range a.Events {
		if e.Kind == "assistant_text" {
			var p struct {
				Text string
				Done bool
			}
			if json.Unmarshal(e.Payload, &p) == nil && p.Done && strings.Contains(p.Text, "restart-drill-committed") {
				replies++
			}
		}
		if e.Kind == "turn_done" {
			var p struct {
				StopReason string `json:"stop_reason"`
			}
			if json.Unmarshal(e.Payload, &p) == nil && p.StopReason == "end_turn" {
				done++
			}
		}
	}
	if replies != 1 || done != 1 {
		return errors.New("expected exactly one fixture reply and successful turn")
	}
	return nil
}
func git(ctx context.Context, path string, args ...string) (string, error) {
	//nolint:gosec // Executable and vocabulary are fixed; path is validated fixture state.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("fixture Git assertion failed")
	}
	return strings.TrimSpace(string(out)), nil
}
func (f *fixture) operation(ctx context.Context, action string) (any, error) {
	if action == "regression" {
		return regressionReceipt(dataDir + "/session-regression/summary.json")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var a turnState
	if err := readJSON(dataDir+"/active.json", &a); err != nil {
		return nil, err
	}
	switch action {
	case "identity":
		var d daemonState
		err := readJSON(dataDir+"/daemon.json", &d)
		return object{"daemon_boot": d.Boot, "engine": a.Engine}, err
	case "armed":
		if a.Path == "" {
			return nil, errors.New("mid-turn barrier not reached")
		}
		raw, err := snapshot(ctx, a)
		if err != nil {
			return nil, err
		}
		var record struct {
			Session string `json:"session_id"`
			WorkDir string `json:"work_dir"`
			Native  string `json:"claude_session_id"`
			Active  bool   `json:"turn_active"`
		}
		if err = readJSON(filepath.Join(dataDir, "state", "agents", a.Session+".json"), &record); err != nil || record.Session != a.Session || record.WorkDir != a.Path || !record.Active || record.Native == "" || record.Native != a.Native {
			return nil, errors.New("durable mid-turn daemon record missing")
		}
		a.OriginalEngine = a.Engine
		a.Prefix = raw
		if err = save(dataDir, "active.json", a); err != nil {
			return nil, err
		}
		var d daemonState
		if err = readJSON(dataDir+"/daemon.json", &d); err != nil {
			return nil, err
		}
		return object{"barrier": true, "session": a.Session, "daemon_boot": d.Boot, "engine": a.Engine, "native": a.Native, "active_record": true}, nil
	case "kill":
		var d daemonState
		if err := readJSON(dataDir+"/daemon.json", &d); err != nil {
			return nil, err
		}
		exe, err := os.Readlink("/proc/" + strconv.Itoa(d.PID) + "/exe")
		if err != nil || exe != "/usr/local/bin/blerg-runner-daemon" {
			return nil, errors.New("owned daemon process identity mismatch")
		}
		return object{"killed": true}, syscall.Kill(d.PID, syscall.SIGKILL)
	case "recover":
		// Wait for production recovery to reopen the same native turn; never inject a second turn.
		f.mu.Unlock()
		err := until(ctx, func() (bool, error) {
			ok, e := connected(ctx)
			if e != nil || !ok {
				return false, e
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			var next turnState
			if e = readJSON(dataDir+"/active.json", &next); e != nil {
				return false, e
			}
			return next.Engine != "" && next.OriginalEngine != "" && next.Engine != next.OriginalEngine, nil
		})
		f.mu.Lock()
		return object{"automatic_recovery": true}, err

	case "release":
		a.Released = true
		return object{"released": true}, save(dataDir, "active.json", a)
	case "verify":
		raw, err := snapshot(ctx, a)
		if err != nil {
			return nil, err
		}
		if err = eventProof(a.Prefix, raw); err != nil {
			return nil, err
		}
		content, err := os.ReadFile(filepath.Join(a.Path, "restart-drill.txt"))
		if err != nil || string(content) != "restart-drill marker\n" {
			return nil, errors.New("workspace marker missing")
		}
		count, err := git(ctx, a.Path, "rev-list", "--count", a.Base+"..HEAD")
		if err != nil || count != "1" {
			return nil, errors.New("expected exactly one committed marker")
		}
		status, err := git(ctx, a.Path, "status", "--porcelain")
		if err != nil || status != "" {
			return nil, errors.New("workspace not clean")
		}
		if err = runnerCall(ctx, "POST", "/api/runner/sessions/"+a.Session+"/stop", object{}, nil); err != nil {
			return nil, err
		}
		f.mu.Unlock()
		err = until(ctx, func() (bool, error) {
			var status struct{ Lifecycle string }
			e := runnerCall(ctx, "GET", "/api/runner/sessions/"+a.Session, nil, &status)
			return e == nil && status.Lifecycle == "ended", nil
		})
		f.mu.Lock()
		if err != nil {
			return nil, err
		}
		return object{"same_session": true, "durable_prefix": true, "marker_retained": true, "one_commit": true, "one_successful_turn": true, "writer_lock": true}, nil
	default:
		return nil, errors.New("unknown fixture action")
	}
}
func (f *fixture) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /{action}", func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("DRILL_CONTROL_KEY") == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Drill-Key")), []byte(os.Getenv("DRILL_CONTROL_KEY"))) != 1 {
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Minute)
		defer cancel()
		action := r.PathValue("action")
		var value any
		var err error
		switch action {
		case "begin":
			var b struct{ Case string }
			if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&b) != nil {
				err = errors.New("bad case")
			} else {
				err = f.begin(ctx, b.Case)
			}
		case "barrier":
			var a turnState
			if json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&a) != nil {
				err = errors.New("bad barrier")
			} else {
				err = f.barrier(ctx, a)
			}
		default:
			value, err = f.operation(ctx, action)
		}
		if err != nil {
			http.Error(w, "fixture assertion not satisfied", http.StatusConflict)
			return
		}
		if value == nil {
			value = object{"ok": true}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encode(value))
	})
	return mux
}
func serveFixture() error {
	if os.Getenv("DRILL_CONTROL_KEY") == "" {
		return errors.New("missing fixture authorization")
	}
	f := &fixture{}
	server := &http.Server{Addr: ":8099", Handler: f.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 9 * time.Minute}
	return server.ListenAndServe()
}
func control(ctx context.Context, action string, body any) (json.RawMessage, error) {
	//nolint:gosec // Destination is fixed loopback; fixtureControlURL is replaced only by offline tests.
	req, err := http.NewRequestWithContext(ctx, "POST", fixtureControlURL+"/"+action, bytes.NewReader(encode(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Drill-Key", os.Getenv("DRILL_CONTROL_KEY"))
	client := safeClient()
	client.Timeout = 9 * time.Minute
	resp, err := client.Do(req) //nolint:gosec // Fixed loopback fixture with redirects disabled.
	if err != nil {
		return nil, errors.New("fixture unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, errors.New("fixture assertion pending or failed")
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}
func runDaemon(ctx context.Context) error {
	for _, d := range []string{"state", "home", "repos", "tmux"} {
		if err := os.MkdirAll(dataDir+"/"+d, 0700); err != nil {
			return err
		}
	}
	if err := until(ctx, func() (bool, error) {
		_, err := os.Stat(dataDir + "/state/desktop-credential.json")
		return err == nil, nil
	}); err != nil {
		return err
	}
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "/usr/local/bin/blerg-runner-daemon")
		cmd.Env = append(os.Environ(), "HOME=/data/home", "TMUX_TMPDIR=/data/tmux", "ANTHROPIC_API_KEY=scripted-fixture-only", "BLERG_RUNNER_REPOS_ROOT=/data/repos", "BLERG_RUNNER_DAEMON_STATE_DIR=/data/state", "BLERG_RUNNER_SERVER_URL=ws://blerg-runner:8080/ws/daemon", "BLERG_RUNNER_SERVER_HTTP=http://blerg-runner:8080", "BLERG_RUNNER_DAEMON_NAME=restart-drill", "BLERG_RUNNER_CLAUDE_USAGE=0")
		if err := cmd.Start(); err != nil {
			return err
		}
		if err := save(dataDir, "daemon.json", daemonState{PID: cmd.Process.Pid, Boot: random()}); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
		_ = cmd.Wait()
	}
	return nil
}
func engineTurn(ctx context.Context, native string) error {
	var a turnState
	if err := readJSON(dataDir+"/active.json", &a); err != nil {
		return err
	}
	path, err := os.Getwd()
	if err != nil {
		return err
	}
	resolved, resolveErr := filepath.EvalSymlinks(path)
	if resolveErr != nil || resolved != path || !strings.HasPrefix(path, dataDir+"/repos/.scratch-") {
		return errors.New("unexpected engine workspace")
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err = git(ctx, path, "init", "-q"); err != nil {
			return err
		}
		if _, err = git(ctx, path, "-c", "user.name=Restart fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "Fixture baseline"); err != nil {
			return err
		}
	}
	// A kernel lock proves no old and new engine write concurrently across restart.
	lock, err := os.OpenFile(filepath.Join(path, ".git", "restart-drill.lock"), os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // Canonical scratch path verified above, within the owned fixture volume.
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("duplicate writer")
	}
	base, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if a.Base != "" {
		base = a.Base
	}
	if a.Path == "" {
		if err = os.WriteFile(filepath.Join(path, "restart-drill.txt"), []byte("restart-drill marker\n"), 0600); err != nil {
			return err
		}
	} else {
		marker, err := os.ReadFile(filepath.Join(path, "restart-drill.txt")) //nolint:gosec // Canonical fixture scratch path validated before recovery.
		if err != nil || string(marker) != "restart-drill marker\n" {
			return errors.New("uncommitted marker lost across restart")
		}
	}
	sid := os.Getenv("BLERG_RUNNER_SESSION_ID")
	if sid == "" {
		return errors.New("session binding missing")
	}
	if _, err = control(ctx, "barrier", turnState{Session: sid, Path: path, Base: base, Engine: random(), Native: native}); err != nil {
		return err
	}
	count, err := git(ctx, path, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return err
	}
	if count == "0" {
		if _, err = git(ctx, path, "add", "--", "restart-drill.txt"); err != nil {
			return err
		}
		if _, err = git(ctx, path, "-c", "user.name=Restart fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "Restart drill marker"); err != nil {
			return err
		}
	}
	return nil
}
func engine(ctx context.Context, args []string) error {
	for _, a := range args {
		if a == "--version" {
			fmt.Println("2.1.47 (Claude Code)")
			return nil
		}
		if a == "--help" {
			fmt.Println("--include-partial-messages --forward-subagent-text --input-format --replay-user-messages")
			return nil
		}
	}
	emit := func(v any) { fmt.Println(string(encode(v))) }
	sid := uuid()
	resuming := false
	for i, arg := range args {
		if (arg == "--session-id" || arg == "--resume") && i+1 < len(args) {
			sid = args[i+1]
			resuming = arg == "--resume"
		}
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9-]+$`).MatchString(sid) {
		return errors.New("invalid native fixture identity")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	history := filepath.Join(home, ".claude", "projects", "restart-fixture")
	if err = os.MkdirAll(history, 0700); err != nil {
		return err
	}
	historyFile := filepath.Join(history, sid+".jsonl")
	if resuming {
		if _, err = os.Stat(historyFile); err != nil { //nolint:gosec // Native ID is restricted to alphanumerics/hyphens before joining the private fixture history path.
			return errors.New("native fixture history lost")
		}
	} else {
		if err = os.WriteFile(historyFile, append(encode(object{"sessionId": sid, "type": "system", "fixture": true}), '\n'), 0600); err != nil { //nolint:gosec // Native ID is restricted above; file stays under the private fixture home.
			return err
		}
	}

	emit(object{"type": "system", "subtype": "init", "session_id": sid})
	turn := func() error {
		if err := engineTurn(ctx, sid); err != nil {
			return err
		}
		emit(object{"type": "assistant", "message": object{"content": []any{object{"type": "text", "text": "restart-drill-committed"}}}})
		emit(object{"type": "result", "subtype": "success", "is_error": false, "session_id": sid, "result": "restart-drill-committed"})
		return nil
	}
	streaming := false
	for _, a := range args {
		if a == "--input-format" {
			streaming = true
		}
	}
	if !streaming {
		return turn()
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	for scanner.Scan() {
		var message object
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			return errors.New("invalid engine input")
		}
		switch message["type"] {
		case "control_request":
			emit(object{"type": "control_response", "response": object{"subtype": "success", "request_id": message["request_id"], "response": object{"mcpServers": []any{}, "still_queued": []any{}}}})
		case "user":
			message["isReplay"] = true
			emit(message)
			if err := turn(); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
func fixtureMain(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	switch args[0] {
	case "fixture":
		return serveFixture()
	case "engine":
		return engine(ctx, args[1:])
	case "daemon":
		return runDaemon(ctx)
	case "init-db":
		ctx, done := context.WithTimeout(ctx, 5*time.Minute)
		defer done()
		return initDB(ctx)
	case "control":
		if len(args) < 2 {
			return errors.New("missing control action")
		}
		var body any
		if args[1] == "begin" {
			if len(args) != 3 {
				return errors.New("missing case")
			}
			body = object{"Case": args[2]}
		}
		raw, err := control(ctx, args[1], body)
		if err == nil {
			fmt.Println(string(raw))
		}
		return err
	}
	return errors.New("unknown mode")
}

func regressionReceipt(path string) (any, error) {
	var rows []struct {
		Name    string
		Passed  bool
		Seconds float64
	}
	if err := readJSON(path, &rows); err != nil {
		return nil, err
	}
	expected := map[string]bool{"process-shapes": true, "parallel-commits": true, "idle-respawn": true, "restart-failed-freeze": true, "restart-child": true, "discard-cleanup": true, "discard-delivery": true, "child-attention": true, "child-escalation-restart": true}
	if len(rows) != len(expected) {
		return nil, errors.New("incomplete regression campaign")
	}
	for _, r := range rows {
		if !r.Passed || !expected[r.Name] {
			return nil, errors.New("missing or failed regression scenario")
		}
		delete(expected, r.Name)
	}
	return object{"passed": 9, "scenarios": rows}, nil
}
