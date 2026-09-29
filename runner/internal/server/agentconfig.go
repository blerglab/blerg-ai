package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// maxConfigBundle bounds the stored agent-config bundle (matches the
// daemon-side build cap plus tar overhead).
const maxConfigBundle = 80 << 20

// agentConfigPath resolves where the uploaded bundle lives. BLERG_RUNNER_DATA_DIR
// defaults to ./data; the bundle is disposable state (daemons re-upload on
// every connect), so an emptyDir is fine in k8s.
func agentConfigPath() string {
	dir := os.Getenv("BLERG_RUNNER_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	return filepath.Join(dir, "agent-config.tar.gz")
}

// HandlePostAgentConfig stores the daemon-uploaded config bundle (bearer:
// daemon token). Runner pods download it at startup so skills/plugins/user
// CLAUDE.md exist inside the pod.
func (a *API) HandlePostAgentConfig(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxConfigBundle+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read error")
		return
	}
	if len(data) > maxConfigBundle {
		writeError(w, http.StatusRequestEntityTooLarge, "bundle too large")
		return
	}
	path := agentConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}
	// Unique temp per request: concurrent daemon uploads (common right after
	// a server restart) must never interleave into one file.
	tmp, err := os.CreateTemp(filepath.Dir(path), "agent-config-*.tmp")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleGetAgentConfig serves the stored bundle to runner pods (bearer:
// daemon token). 404 when no daemon has uploaded yet.
func (a *API) HandleGetAgentConfig(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	f, err := os.Open(agentConfigPath())
	if err != nil {
		writeError(w, http.StatusNotFound, "no agent config uploaded")
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = io.Copy(w, f)
}
