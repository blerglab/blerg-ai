package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// maxConfigBundle bounds the explicitly supplied operator archive.
const maxConfigBundle = 80 << 20

// HandlePostAgentConfig retires the old anonymous desktop upload endpoint.
func (a *API) HandlePostAgentConfig(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeError(w, http.StatusGone, "automatic desktop config sharing has been retired")
}

// sharedAgentConfigPath is an explicit operator source, never a desktop upload
// or the historical install-wide cache. Its contents are intentionally shared.
func sharedAgentConfigPath() string {
	path := os.Getenv("BLERG_RUNNER_SHARED_AGENT_CONFIG_BUNDLE")
	if !filepath.IsAbs(path) {
		return ""
	}
	return path
}

// HandleGetAgentConfig serves only the explicitly configured operator archive
// to authenticated runtimes. Historical desktop uploads are never read.
func (a *API) HandleGetAgentConfig(w http.ResponseWriter, r *http.Request) {
	if !a.isDaemonToken(a.rawBearer(r)) {
		if _, ok := a.authDataPlane(w, r); !ok {
			return
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	f, err := os.Open(sharedAgentConfigPath())
	if err != nil {
		writeError(w, http.StatusNotFound, "no agent config uploaded")
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigBundle {
		writeError(w, http.StatusNotFound, "shared config unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = io.Copy(w, f)
}
