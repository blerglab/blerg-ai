// Package updatehandoff records same-user planned updates in the daemon's state
// directory. This is recovery bookkeeping, not a privilege boundary: host
// sessions already run as the person and can exercise that person's authority.
package updatehandoff

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const MaxAge = 5 * time.Minute
const pendingFile = "update-pending.json"

// Request comes only from a live tool event observed by the running daemon.
type Request struct {
	SessionID   string          `json:"session_id"`
	Call        json.RawMessage `json:"call"`
	EnginePID   int             `json:"engine_pid"`
	EngineStart string          `json:"engine_start"`
}

type Receipt struct {
	Request      Request   `json:"request"`
	CreatedAt    time.Time `json:"created_at"`
	BinarySHA256 string    `json:"binary_sha256"`
	Commit       string    `json:"commit"`
	Succeeded    bool      `json:"-"` // derived by the replacement daemon, never asserted by the installer
}

func (r *Receipt) Fresh(now time.Time) bool {
	return r != nil && r.Succeeded && r.Request.SessionID != "" && len(r.Request.Call) > 0 && !now.Before(r.CreatedAt) && now.Sub(r.CreatedAt) < MaxAge
}

func Digest(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // installer-selected executable; same-user recovery bookkeeping
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Take consumes even invalid evidence. The running executable and embedded
// commit must match the planned target; merely restarting an old binary fails.
func Take(dir, binary, commit string, planned bool) *Receipt {
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, pendingFile)
	claimed := path + ".claimed"
	if os.Rename(path, claimed) != nil {
		return nil
	}
	defer func() { _ = os.Remove(claimed); _ = syncDir(dir) }()
	if syncDir(dir) != nil {
		return nil
	}
	info, err := os.Lstat(claimed)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil
	}
	f, err := os.Open(claimed) //nolint:gosec // fixed filename in daemon state dir
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var r Receipt
	if json.NewDecoder(io.LimitReader(f, 65536)).Decode(&r) != nil || !planned || commit == "" || commit == "dev" || r.Commit != commit {
		return nil
	}
	digest, err := Digest(binary)
	if err != nil || digest != r.BinarySHA256 {
		return nil
	}
	r.Succeeded = true
	if !r.Fresh(time.Now()) {
		return nil
	}
	return &r
}

func saveJSON(dir, name string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".update-*")
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.Write(b); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}
func syncDir(dir string) error {
	f, err := os.Open(dir) //nolint:gosec // G304: sync the configured daemon state directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// Observer retains only observations from this process; disk-loaded recovery
// records are never enough to attribute an installer.
type Observer struct {
	mu       sync.Mutex
	requests map[string]Request
	attempts map[string]attempt
}

func (o *Observer) Observe(r Request) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.requests == nil {
		o.requests = make(map[string]Request)
	}
	if len(r.Call) == 0 {
		delete(o.requests, r.SessionID)
		delete(o.attempts, r.SessionID)
	} else {
		o.requests[r.SessionID] = r
	}
}
func (o *Observer) requester(pid int) *Request {
	o.mu.Lock()
	defer o.mu.Unlock()
	tree := ancestors(pid)
	// setsid/nohup can reparent to init before the installer contacts us. Read
	// the daemon-assigned identity inherited by the process from the OS, then
	// compare it with the still-live engine. No session/call IDs arrive over RPC.
	inherited := processSession(pid)
	var result *Request
	for _, r := range o.requests {
		if r.EnginePID <= 1 || r.EngineStart == "" || ProcessStart(r.EnginePID) != r.EngineStart {
			continue
		}
		direct := tree[r.EnginePID] == r.EngineStart
		detached := inherited != "" && inherited == r.SessionID && processSession(r.EnginePID) == inherited
		if !direct && !detached {
			continue
		}
		if result != nil {
			return nil
		}
		copyOf := r
		result = &copyOf
	}
	return result
}

type attempt struct {
	request Request
	parents map[int]string
	started time.Time
}

type wireRequest struct {
	Binary string `json:"binary"`
	Commit string `json:"commit"`
	Cancel bool   `json:"cancel"`
	Begin  bool   `json:"begin"`
}

// Serve is started only by the daemon holding the agent-record lock. The socket
// and all evidence stay in its owner-only state directory. Close joins the loop.
func (o *Observer) Serve(dir string) (func(), error) {
	if dir == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil { //nolint:gosec // G302: owner-only directory requires search permission
		return nil, err
	}
	path := filepath.Join(dir, "update.sock")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			o.handle(c, dir)
		}
	}()
	return func() { _ = ln.Close(); <-done }, nil
}
func (o *Observer) handle(c *net.UnixConn, dir string) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	pid, err := peerPID(c)
	if err != nil {
		return
	}
	var req wireRequest
	if json.NewDecoder(io.LimitReader(c, 16384)).Decode(&req) != nil {
		return
	}
	err = o.prepare(dir, pid, req)
	response := ""
	if err != nil {
		response = err.Error()
	}
	_ = json.NewEncoder(c).Encode(response)
}
func (o *Observer) prepare(dir string, pid int, req wireRequest) error {
	if req.Cancel {
		// Cancel only a matching target; an older build cannot erase a different target.
		b, err := os.ReadFile(filepath.Join(dir, pendingFile)) //nolint:gosec // G304: fixed pending-update name in daemon state directory
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		var r Receipt
		if json.Unmarshal(b, &r) != nil {
			return errors.New("invalid pending update")
		}
		if req.Commit == r.Commit {
			return os.Remove(filepath.Join(dir, pendingFile))
		}
		return nil
	}
	if !req.Begin {
		_ = os.Remove(filepath.Join(dir, pendingFile))
	}
	r := o.requester(pid)
	if r == nil {
		return errors.New("update has no unique live tool call")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if req.Begin {
		if o.attempts == nil {
			o.attempts = make(map[string]attempt)
		}
		parents := ancestors(pid)
		// Only installer/tool ancestors below the observed engine count;
		// the engine and daemon are shared by successive calls.
		for ancestor := range ancestors(r.EnginePID) {
			delete(parents, ancestor)
		}
		delete(parents, pid) // the short-lived notification helper itself
		o.attempts[r.SessionID] = attempt{request: *r, parents: parents, started: time.Now()}
		return nil
	}
	started, ok := o.attempts[r.SessionID]
	delete(o.attempts, r.SessionID)
	// A rejected update must not leave a previous target available for recovery.
	_ = os.Remove(filepath.Join(dir, pendingFile))
	if !ok || time.Since(started.started) > 45*time.Minute || !bytes.Equal(started.request.Call, r.Call) || started.request.EnginePID != r.EnginePID || started.request.EngineStart != r.EngineStart {
		return errors.New("installer did not start in this live tool call")
	}
	related := false
	for ancestor, start := range ancestors(pid) {
		if start != "" && started.parents[ancestor] == start {
			related = true
			break
		}
	}
	if !related {
		return errors.New("installer process ancestry changed")
	}
	if req.Commit == "" || req.Commit == "dev" {
		return errors.New("update has no build commit")
	}
	digest, err := Digest(req.Binary)
	if err != nil {
		return err
	}
	if err := saveJSON(dir, pendingFile, Receipt{Request: *r, CreatedAt: time.Now().UTC(), BinarySHA256: digest, Commit: req.Commit}); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "restart-planned"), []byte("update\n"), 0600)
}

// Notify sends only the target binary's identity. It does not write recovery
// evidence, and accepts no session ID, call ID, command, or success assertion.
func Begin(dir string) error { return notify(dir, wireRequest{Begin: true}) }
func Notify(dir, binary, commit string, cancel bool) error {
	return notify(dir, wireRequest{Binary: binary, Commit: commit, Cancel: cancel})
}
func notify(dir string, request wireRequest) error {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	c, err := dialer.DialContext(context.Background(), "unix", filepath.Join(dir, "update.sock"))
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if err = json.NewEncoder(c).Encode(request); err != nil {
		return err
	}
	var response string
	if err = json.NewDecoder(c).Decode(&response); err != nil {
		return err
	}
	if response != "" {
		return errors.New(response)
	}
	return nil
}
