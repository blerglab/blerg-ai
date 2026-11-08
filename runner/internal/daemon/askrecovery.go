package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const (
	questionRecoveryTimeout  = 10 * time.Second
	questionRecoveryBackoff  = 5 * time.Second
	questionRecoveryAttempts = 6
)

func orDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

// hostGenerationEnv tells a session's CLI which hosting it runs under.
const hostGenerationEnv = "BLERG_RUNNER_HOST_GENERATION"

// hostGenerationFile holds the last generation minted, beside the agent records. Written before
// the generation is used, so an engine never runs under one a restart could reissue.
const hostGenerationFile = "host-generation"

// nextHostGeneration orders hostings. It always rises past the last one minted, kept across
// restarts in cfg.GenerationDir, so a clock that steps back cannot reorder them. The clock only
// supplies a starting point where nothing was kept: a new host, or a pod's fresh home, where
// floor (what the server has seen for the session) keeps the order.
func (h *AgentHost) nextHostGeneration(floor int64) int64 {
	h.hostGenerationMu.Lock()
	defer h.hostGenerationMu.Unlock()
	path := ""
	if h.cfg.GenerationDir != "" {
		path = filepath.Join(filepath.Dir(h.cfg.GenerationDir), hostGenerationFile)
	}
	last := h.hostGeneration.Load()
	if !h.hostGenerationLoaded {
		h.hostGenerationLoaded = true
		last = max(last, readHostGeneration(path))
	}
	now := time.Now
	if h.hostGenerationClock != nil {
		now = h.hostGenerationClock
	}
	next := max(now().UnixNano(), last+1, floor+1)
	h.hostGeneration.Store(next)
	if err := writeHostGeneration(path, next); err != nil {
		log.Printf("agent host: saving the host generation: %v", err)
	}
	return next
}

func readHostGeneration(path string) int64 {
	if path == "" {
		return 0
	}
	raw, err := os.ReadFile(path) //nolint:gosec // the daemon's own state file
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("agent host: reading the host generation: %v", err)
		}
		return 0
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		log.Printf("agent host: ignoring an unreadable host generation: %v", err)
		return 0
	}
	return value
}

// writeHostGeneration replaces the file in one step and syncs it: a crash leaves the old value or
// the new one.
func writeHostGeneration(path string, value int64) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), hostGenerationFile+".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err = tmp.WriteString(strconv.FormatInt(value, 10)); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func hostedSessionEnv(env []string, generation int64) []string {
	return append(env, hostGenerationEnv+"="+strconv.FormatInt(generation, 10))
}

// recoveryToken is the host's own credential. A session token would not do: the agent holds it.
func (h *AgentHost) recoveryToken() string {
	if h.cfg.DaemonTokenSource != nil {
		if token := h.cfg.DaemonTokenSource(); token != "" {
			return token
		}
	}
	return h.cfg.DaemonToken
}

// recoverQuestionWaiters withdraws the asks of a session's previous hostings: their
// in-flight provider turn and the HTTP polls are gone. msg's generation is the boundary,
// so a retry that lands late never reaches a question of this hosting. An older server
// has no such route, and there the question stays as it always did.
func (h *AgentHost) recoverQuestionWaiters(ctx context.Context, msg protocol.SpawnSession) error {
	token := h.recoveryToken()
	if h.cfg.ServerHTTP == "" || token == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, orDefault(h.questionRecoveryWait, questionRecoveryTimeout))
	defer cancel()
	body, _ := json.Marshal(map[string]any{"session_id": msg.SessionID, "host_generation": msg.HostGeneration})
	request, err := http.NewRequestWithContext(ctx, "POST", h.cfg.ServerHTTP+"/api/messages/recover", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK, http.StatusNotFound, http.StatusMethodNotAllowed:
		return nil
	}
	return fmt.Errorf("question recovery refused (%d)", response.StatusCode)
}

// recoverOrDeferQuestions never stops a session from being hosted again: a failed
// recovery only leaves dead questions open until a later attempt succeeds.
func (h *AgentHost) recoverOrDeferQuestions(ctx context.Context, msg protocol.SpawnSession) {
	err := h.recoverQuestionWaiters(ctx, msg)
	if err == nil {
		return
	}
	log.Printf("agent session %s: question recovery deferred: %v", msg.SessionID, err)
	h.mu.Lock()
	if h.questionRecovery == nil {
		h.questionRecovery = map[string]protocol.SpawnSession{}
	}
	h.questionRecovery[msg.SessionID] = msg
	h.mu.Unlock()
	go h.retryQuestionRecovery() //nolint:gosec,contextcheck // outlives the spawn; each attempt has its own timeout
}

// retryQuestionRecovery runs after a deferral and on every connection, with a
// bounded number of attempts: what still fails waits for the next connection.
func (h *AgentHost) retryQuestionRecovery() {
	h.retryWaiterNotices(context.Background())
	for attempt := 0; attempt < questionRecoveryAttempts; attempt++ {
		if attempt > 0 {
			time.Sleep(orDefault(h.questionRecoveryPause, questionRecoveryBackoff))
		}
		h.mu.Lock()
		pending := make([]protocol.SpawnSession, 0, len(h.questionRecovery))
		for _, msg := range h.questionRecovery {
			pending = append(pending, msg)
		}
		h.mu.Unlock()
		if len(pending) == 0 {
			return
		}
		for _, msg := range pending {
			if err := h.recoverQuestionWaiters(context.Background(), msg); err != nil {
				log.Printf("agent session %s: question recovery: %v", msg.SessionID, err)
				continue
			}
			h.mu.Lock()
			if current, ok := h.questionRecovery[msg.SessionID]; ok && current.HostGeneration == msg.HostGeneration {
				delete(h.questionRecovery, msg.SessionID)
			}
			h.mu.Unlock()
		}
	}
}
