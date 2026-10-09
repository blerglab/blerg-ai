package server

// The UI package the runner distributes (packages.go): the tarball the frontend build packed,
// served immutably by the name the manifest file gives it, advertised in /agents.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packagesFixture writes a packed package the way the frontend build does (manifest.json, the
// tarball and the README beside it) and loads it, restoring the empty state afterwards.
func packagesFixture(t *testing.T) (dir string, tarball []byte) {
	t.Helper()
	dir = t.TempDir()
	tarball = []byte("\x1f\x8b\x08\x00not really a tarball")
	if err := os.WriteFile(filepath.Join(dir, "blerglab-chat-0.1.0.tgz"), tarball, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# @blerglab/chat\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"packages":[{"name":"@blerglab/chat","version":"0.1.0","file":"blerglab-chat-0.1.0.tgz","sha512":"sha512-AAAA","readme":"README.md"}]}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadPackages(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadPackages(filepath.Join(dir, "none")) })
	return dir, tarball
}

func packagesGet(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	agentContractMux(&API{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The tarball is served by its manifest name, immutable, as gzip; the index lists it with its
// URL; the README is served as Markdown; anything else — another name, a path — is a 404.
func TestPackagesRoutes(t *testing.T) {
	_, tarball := packagesFixture(t)

	rec := packagesGet(t, "/packages/blerglab-chat-0.1.0.tgz")
	if rec.Code != http.StatusOK || rec.Body.String() != string(tarball) {
		t.Fatalf("tarball = %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control = %q", cc)
	}

	rec = packagesGet(t, "/packages/")
	if rec.Code != http.StatusOK {
		t.Fatalf("index = %d: %s", rec.Code, rec.Body.String())
	}
	var index struct {
		Packages []struct {
			Name, Version, File, SHA512, URL string
		} `json:"packages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Packages) != 1 || index.Packages[0].Name != "@blerglab/chat" || index.Packages[0].Version != "0.1.0" ||
		index.Packages[0].SHA512 != "sha512-AAAA" || index.Packages[0].URL != "http://example.com/packages/blerglab-chat-0.1.0.tgz" {
		t.Fatalf("index = %+v", index.Packages)
	}
	if strings.Contains(rec.Body.String(), "readme") {
		t.Errorf("the README's file name is the build's business, not the index's: %s", rec.Body.String())
	}

	rec = packagesGet(t, "/packages/@blerglab/chat/README.md")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), "# @blerglab/chat") {
		t.Fatalf("README = %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("README Content-Type = %q", ct)
	}

	for _, path := range []string{
		"/packages/other-0.1.0.tgz",
		"/packages/manifest.json",
		"/packages/README.md",
		"/packages/..%2Fmanifest.json",
		"/packages/@blerglab/other/README.md",
		"/packages/blerglab-chat-0.1.0.tgz/",
	} {
		if rec := packagesGet(t, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, rec.Code)
		}
	}
}

// With no manifest file there is no package: the routes answer 404 and /agents carries no ui.
func TestPackagesAbsent(t *testing.T) {
	if err := LoadPackages(filepath.Join(t.TempDir(), "none")); err != nil {
		t.Fatalf("a missing manifest is not an error: %v", err)
	}
	if rec := packagesGet(t, "/packages/"); rec.Code != http.StatusNotFound {
		t.Errorf("index = %d, want 404", rec.Code)
	}
	if e := RunnerManifest("https://runner.example.test"); e.UI != nil {
		t.Errorf("ui = %+v, want absent", e.UI)
	}
	rec := packagesGet(t, "/agents")
	if strings.Contains(rec.Body.String(), `"ui"`) {
		t.Errorf("/agents carries ui with no package: %s", rec.Body.String())
	}
}

// With a packed package, /agents tells an app what to install, from where, and where to read.
func TestPackagesInManifest(t *testing.T) {
	packagesFixture(t)
	e := RunnerManifest("https://runner.example.test/")
	if e.UI == nil || e.UI.ChatPackage == nil {
		t.Fatal("ui.chat_package absent")
	}
	p := e.UI.ChatPackage
	if p.Name != "@blerglab/chat" || p.Version != "0.1.0" || p.SHA512 != "sha512-AAAA" ||
		p.URL != "https://runner.example.test/packages/blerglab-chat-0.1.0.tgz" ||
		p.DocsURL != "https://runner.example.test/packages/@blerglab/chat/README.md" {
		t.Fatalf("chat_package = %+v", p)
	}
	rec := packagesGet(t, "/agents")
	var got struct {
		UI struct {
			ChatPackage struct{ URL string } `json:"chat_package"`
		} `json:"ui"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.UI.ChatPackage.URL != "http://example.com/packages/blerglab-chat-0.1.0.tgz" {
		t.Errorf("/agents ui.chat_package.url = %q", got.UI.ChatPackage.URL)
	}
}
