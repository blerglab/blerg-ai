package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// A notice contains no credential or answer. Keep it before canceling the poll;
// reconnect replays it using the host's current credential, even after a restart.
type waiterNotice struct {
	Base     string `json:"base"`
	Session  string `json:"session"`
	Question string `json:"question"`
}

func (m *httpMessenger) saveWaiterNotice(id string) (string, error) {
	dir := filepath.Join(DaemonStateDir(), "waiter-notices")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	raw, err := json.Marshal(waiterNotice{m.base, m.sessionID, id})
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%x.json", sha256.Sum256(raw)))
	f, err := os.CreateTemp(dir, "notice-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	directory, err := os.Open(dir) //nolint:gosec // private daemon state directory; no caller-supplied path
	if err != nil {
		return "", err
	}
	err = directory.Sync()
	closeErr = directory.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	return path, nil
}

func (m *httpMessenger) sendWaiterNotice(ctx context.Context, id string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base+"/api/messages/"+url.PathEscape(id)+"/expire", bytes.NewBufferString(`{"waiter_ended":true}`))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.authToken())
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK {
		return true
	}
	// Authentication may recover when the daemon rotates its token. Deleted
	// questions and revoked scope are terminal; retrying cannot repair them.
	return resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests
}

func (m *httpMessenger) retryWaiterNotice(id, path string) {
	m.retryWaiterNoticeAttempts(context.Background(), id, path, 0, questionRecoveryBackoff)
}

// Match question recovery's bounded retry batch. Keep the durable notice after
// transient failures; the next connection retries with the current credential.
func (m *httpMessenger) retryWaiterNoticeAttempts(ctx context.Context, id, path string, first int, pause time.Duration) {
	for attempt := first; attempt < questionRecoveryAttempts; attempt++ {
		if path != "" {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return
			}
		}
		if attempt > 0 {
			wait := m.waitNotice
			if wait == nil {
				wait = waitNoticeTimer
			}
			if !wait(ctx, pause*time.Duration(1<<min(attempt-1, 2))) {
				return
			}
		}
		if m.sendWaiterNotice(ctx, id) {
			if path != "" {
				_ = os.Remove(path)
			}
			return
		}
	}
}

func waitNoticeTimer(ctx context.Context, pause time.Duration) bool {
	timer := time.NewTimer(pause)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (h *AgentHost) retryWaiterNotices(ctx context.Context) {
	paths, err := filepath.Glob(filepath.Join(DaemonStateDir(), "waiter-notices", "*.json"))
	if err != nil {
		return
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // glob is confined to the daemon's private notice directory
		if err != nil {
			continue
		}
		var n waiterNotice
		if json.Unmarshal(raw, &n) != nil || n.Base != h.cfg.ServerHTTP {
			continue
		}
		h.mu.Lock()
		if h.waiterNoticeWorkers == nil {
			h.waiterNoticeWorkers = map[string]bool{}
		}
		if h.waiterNoticeWorkers[path] {
			h.mu.Unlock()
			continue
		}
		h.waiterNoticeWorkers[path] = true
		h.mu.Unlock()
		m := &httpMessenger{base: n.Base, sessionID: n.Session, tokenSource: h.recoveryToken, client: http.DefaultClient}
		if m.sendWaiterNotice(ctx, n.Question) {
			if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
				log.Printf("waiter notice receipt: %v", err)
			}
			h.finishWaiterNotice(path)
		} else {
			go func() { //nolint:gosec,contextcheck // persisted notices outlive the connection; every attempt has its own timeout
				defer h.finishWaiterNotice(path)
				m.retryWaiterNoticeAttempts(context.Background(), n.Question, path, 1, questionRecoveryBackoff)
			}()
		}
	}
}

func (h *AgentHost) finishWaiterNotice(path string) {
	h.mu.Lock()
	delete(h.waiterNoticeWorkers, path)
	h.mu.Unlock()
}
