package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg" // decoders for screenshot validation
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Publishing: agents push mockup bundles (HTML/CSS/JS) and screenshots; the
// server stores and serves them. SECURITY: mockup bundles are agent-authored
// HTML/JS and the blerg-runner app origin is unauthenticated — bundles are served
// with a sandbox CSP and, when BLERG_RUNNER_MOCKUP_HOSTS is set, ONLY on those
// dedicated hostnames (never the app origin).
const (
	maxMockupBundle  = 20 << 20
	maxScreenshot    = 8 << 20
	maxMockupEntries = 500
)

func publishDataDir(sub string) string {
	dir := os.Getenv("BLERG_RUNNER_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	return filepath.Join(dir, sub)
}

// mockupHosts returns the allowed serving hostnames ("" entries removed);
// empty slice = no restriction (dev mode).
func mockupHosts() []string {
	raw := os.Getenv("BLERG_RUNNER_MOCKUP_HOSTS")
	if raw == "" {
		return nil
	}
	var out []string
	for _, h := range strings.Split(raw, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, strings.ToLower(h))
		}
	}
	return out
}

func hostAllowed(r *http.Request, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	host := strings.ToLower(r.Host)
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	for _, a := range allowed {
		if host == a {
			return true
		}
	}
	return false
}

func newPublishID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// validPublishID accepts only our own 32-hex-char IDs (128 random bits: the
// published pages are intentionally public, so the id is the only secret).
func validPublishID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// HandlePostMockup accepts a tar.gz of static files (bearer: daemon token)
// and returns the served URL.
func (a *API) HandlePostMockup(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	id := newPublishID()
	dest := filepath.Join(publishDataDir("mockups"), id)
	if err := extractMockupBundle(io.LimitReader(r.Body, maxMockupBundle+1), dest); err != nil {
		_ = os.RemoveAll(dest)
		writeError(w, http.StatusBadRequest, "bundle error: "+err.Error())
		return
	}
	base := os.Getenv("BLERG_RUNNER_MOCKUP_BASE") // e.g. https://m.example.com
	writeJSON(w, http.StatusCreated, map[string]string{
		"id":  id,
		"url": base + "/m/" + id + "/",
	})
}

// extractMockupBundle unpacks a bundle with escape protection and caps.
func extractMockupBundle(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return errors.New("not a gzip stream")
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	total, entries := int64(0), 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		entries++
		if entries > maxMockupEntries {
			return errors.New("too many files")
		}
		p := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(p, root+string(filepath.Separator)) {
			return errors.New("path escape in bundle")
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxMockupBundle))
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > maxMockupBundle {
			return errors.New("bundle too large")
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return err
		}
	}
}

// HandleServeMockup serves published bundles. Sandboxed CSP always; host
// allowlist enforced when configured (separate-origin requirement).
func (a *API) HandleServeMockup(w http.ResponseWriter, r *http.Request) {
	if !hostAllowed(r, mockupHosts()) {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/m/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if !validPublishID(id) {
		http.NotFound(w, r)
		return
	}
	sub := "index.html"
	if len(parts) == 2 && parts[1] != "" {
		sub = parts[1]
	}
	root, err := filepath.Abs(filepath.Join(publishDataDir("mockups"), id))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(root, filepath.FromSlash(sub))
	if !strings.HasPrefix(p, root+string(filepath.Separator)) && p != root {
		http.NotFound(w, r)
		return
	}
	// Agent-authored content: scripts may run, but the document is sandboxed
	// (no same-origin credentials/DOM, no top-level navigation).
	// connect-src 'none': even in dev (no host gating) a bundle cannot
	// fetch() the unauthenticated blerg-runner API.
	w.Header().Set("Content-Security-Policy", "sandbox allow-scripts; default-src 'self' 'unsafe-inline' data: blob:; connect-src 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, p) //nolint:gosec // p is contained in the mockup root by the HasPrefix check above; the id is validated by validPublishID
}

// HandlePostScreenshot accepts a PNG/JPEG (bearer: daemon token), validates
// by decoding (never trusts Content-Type), and returns the served URL.
func (a *API) HandlePostScreenshot(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxScreenshot+1))
	if err != nil || len(data) > maxScreenshot {
		writeError(w, http.StatusBadRequest, "image too large or unreadable")
		return
	}
	// DecodeConfig first: a small file can declare enormous dimensions and
	// the full decode would allocate width*height*4 bytes (decompression
	// bomb). Cap at ~40MP before decoding pixels.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		writeError(w, http.StatusBadRequest, "not a valid PNG or JPEG")
		return
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > 40_000_000 {
		writeError(w, http.StatusBadRequest, "image dimensions out of range")
		return
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		writeError(w, http.StatusBadRequest, "image decode failed")
		return
	}
	ext := ".png"
	if format == "jpeg" {
		ext = ".jpg"
	}
	id := newPublishID() + ext
	dir := publishDataDir("screenshots")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}
	if err := os.WriteFile(filepath.Join(dir, id), data, 0o600); err != nil {
		writeError(w, http.StatusInternalServerError, "storage error")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "url": "/s/" + id})
}

// HandleServeScreenshot serves validated images (inert; may share app origin).
func (a *API) HandleServeScreenshot(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(strings.TrimPrefix(r.URL.Path, "/s/"))
	if name == "" || name == "." {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Join(publishDataDir("screenshots"), name))
}
