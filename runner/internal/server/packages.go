package server

// The UI package the runner distributes (chat-package design, "Distribution"): the frontend
// build packs @blerglab/chat into <frontend dist>/packages/ — the tarball, its README and a
// manifest.json naming them — and the runner serves that directory by name and advertises the
// package in GET /agents (ComponentEntry.UI), so an app learns what to install, how to pin it and
// where to read from the same document that tells it how to get a credential.
//
//	GET /packages/                             {"packages":[{name, version, file, sha512, url}]}
//	GET /packages/{file}                       the tarball: application/gzip, immutable
//	GET /packages/{scope}/{name}/README.md     the package's README as Markdown
//
// Public, like /agents: a tarball is documentation-grade — it carries no secrets and `npm i <url>`
// has no credential to send. Only names the manifest lists are ever served, so no request can
// name a path; a missing manifest means no package, no ui field and a 404 on every route.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
)

// chatPackageName is the package the manifest's ui.chat_package names.
const chatPackageName = "@blerglab/chat"

// packageEntry is one entry of the build's manifest.json.
type packageEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	File    string `json:"file"`
	SHA512  string `json:"sha512"`
	// Readme is the README's file name beside the tarball; "README.md" when absent.
	Readme string `json:"readme,omitempty"`
}

// packagesManifest is manifest.json.
type packagesManifest struct {
	Packages []packageEntry `json:"packages"`
}

// loadedPackages is what LoadPackages found: the directory and the entries that passed validation.
type loadedPackages struct {
	dir      string
	packages []packageEntry
}

// uiPackages holds the loaded manifest. An atomic value for the same reason componentVersion is:
// /agents may be served before and concurrently with the load in main.
var uiPackages atomic.Value

// LoadPackages reads <dir>/manifest.json and makes its packages servable. A missing file is the
// normal state of a build without the package (no routes, no ui field) and is not an error; a
// malformed one is, and leaves nothing loaded.
func LoadPackages(dir string) error {
	uiPackages.Store(&loadedPackages{dir: dir})
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json")) //nolint:gosec // dir is the operator's frontend dist, set in main
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("packages manifest: %w", err)
	}
	var m packagesManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("packages manifest: %w", err)
	}
	for i, p := range m.Packages {
		if p.Name == "" || p.Version == "" || !packageFileName(p.File) || (p.Readme != "" && !packageFileName(p.Readme)) {
			return fmt.Errorf("packages manifest: entry %d (%q) is not a package with a file beside the manifest", i, p.Name)
		}
	}
	uiPackages.Store(&loadedPackages{dir: dir, packages: m.Packages})
	return nil
}

// packageFileName accepts a plain file name: something that lives beside the manifest, never a path.
func packageFileName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, `/\`)
}

func currentPackages() *loadedPackages {
	if p, ok := uiPackages.Load().(*loadedPackages); ok {
		return p
	}
	return &loadedPackages{}
}

// find returns the entry whose tarball is file, or whose package is name.
func (l *loadedPackages) find(match func(packageEntry) bool) *packageEntry {
	for i := range l.packages {
		if match(l.packages[i]) {
			return &l.packages[i]
		}
	}
	return nil
}

// runnerUIInfo is the manifest's ui field for the given public base URL, or nil without a package.
func runnerUIInfo(base string) *agentsmanifest.UIInfo {
	p := currentPackages().find(func(e packageEntry) bool { return e.Name == chatPackageName })
	if p == nil {
		return nil
	}
	return &agentsmanifest.UIInfo{ChatPackage: &agentsmanifest.PackageInfo{
		Name:    p.Name,
		Version: p.Version,
		URL:     base + "/packages/" + p.File,
		SHA512:  p.SHA512,
		DocsURL: base + "/packages/" + p.Name + "/README.md",
	}}
}

// handlePackagesIndex is GET /packages/: the manifest plus each entry's URL as reached by this
// caller. It changes with every deploy, so it is revalidated rather than cached.
func handlePackagesIndex(w http.ResponseWriter, r *http.Request) {
	l := currentPackages()
	if len(l.packages) == 0 {
		writeError(w, http.StatusNotFound, "no packages")
		return
	}
	type entry struct {
		packageEntry
		Readme string `json:"readme,omitempty"` // shadows the manifest's: the file name is the build's business
		URL    string `json:"url"`
	}
	out := make([]entry, 0, len(l.packages))
	for _, p := range l.packages {
		out = append(out, entry{packageEntry: p, URL: runnerOrigin(r) + "/packages/" + p.File})
	}
	w.Header().Set("Cache-Control", "no-cache")
	writeJSON(w, http.StatusOK, map[string]any{"packages": out})
}

// handlePackageFile is GET /packages/{file}: a tarball the manifest names, immutable because its
// name carries the version.
func handlePackageFile(w http.ResponseWriter, r *http.Request) {
	l := currentPackages()
	file := r.PathValue("file")
	p := l.find(func(e packageEntry) bool { return e.File == file })
	if p == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	servePackageFile(w, r, filepath.Join(l.dir, p.File), "application/gzip", "public, max-age=31536000, immutable")
}

// handlePackageReadme is GET /packages/{scope}/{name}/README.md for a scoped package such as
// @blerglab/chat. The docs URL is the same across versions, so it is revalidated, not immutable.
func handlePackageReadme(w http.ResponseWriter, r *http.Request) {
	l := currentPackages()
	name := r.PathValue("scope") + "/" + r.PathValue("name")
	p := l.find(func(e packageEntry) bool { return e.Name == name })
	if p == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	readme := p.Readme
	if readme == "" {
		readme = "README.md"
	}
	servePackageFile(w, r, filepath.Join(l.dir, readme), "text/markdown; charset=utf-8", "no-cache")
}

// servePackageFile serves one validated file of the packages directory with a fixed type: the
// type is ours, so nothing is sniffed, and a file the build did not leave behind is a 404.
func servePackageFile(w http.ResponseWriter, r *http.Request, path, contentType, cacheControl string) {
	f, err := os.Open(path) //nolint:gosec // path is the packages dir joined with a name the manifest lists (packageFileName)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("packages: open %s: %v", path, err)
		}
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", cacheControl)
	http.ServeContent(w, r, "", time.Time{}, f)
}
