package daemon

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Config-bundle limits: pods have no ~/.claude or ~/.hermes, so the daemon
// ships a synced copy of the user's agent config (CLAUDE.md, skills, agent
// defs, one version per cached plugin, hermes's model/provider choice).
// Oversized files are skipped, the whole bundle is capped.
const (
	bundleFileCap  = 5 << 20  // per-file
	bundleTotalCap = 64 << 20 // whole bundle
	bundleEntryCap = 16384    // bound header and tiny-file work
)

// bundleTransferTimeout bounds one upload or download of the bundle, body
// included. Without it a server that accepts the connection and then goes
// quiet would hold the daemon's sync, or a pod's startup, for ever.
const bundleTransferTimeout = 5 * time.Minute

// bundleRoots are the ~/.claude entries included in the bundle (relative to
// homeDir). plugins/cache is handled specially (one version per plugin).
var bundleRoots = []string{
	".claude/CLAUDE.md",
	".claude/skills",
	".claude/agents",
	".claude/personas",
	// Hermes's persistent model/provider selection — not a secret (that's
	// .env, delivered separately via HERMES_ENV_CONTENTS for k8s pods, see
	// k8sjobs.go), just "which model" the operator picked with `hermes
	// model`/`hermes setup`.
	".hermes/config.yaml",
}

// BuildConfigBundle tars the user's agent config for shipping to runner pods.
func BuildConfigBundle(homeDir string) ([]byte, error) {
	homeRoot, err := os.OpenRoot(homeDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = homeRoot.Close() }()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	total := int64(0)
	entries := 0

	addFile := func(rel string, info os.FileInfo) error {
		if !bundleRegularFile(info) || info.Size() > bundleFileCap || !bundlePathWithoutLinks(homeRoot, rel) {
			return nil // skip oversized files
		}
		if total+info.Size() > bundleTotalCap {
			return fmt.Errorf("config bundle exceeds %d bytes", bundleTotalCap)
		}
		f, err := openBundleFile(homeRoot, rel)
		if err != nil {
			return nil //nolint:nilerr // an unreadable file is left out of the bundle, the rest still ships
		}
		opened, statErr := f.Stat()
		if statErr != nil || !bundleRegularFile(opened) || !os.SameFile(info, opened) {
			_ = f.Close()
			return nil //nolint:nilerr // An unreadable or changed file is excluded from the bundle, not exported or fatal to unrelated entries.
		}
		data, readErr := io.ReadAll(io.LimitReader(f, bundleFileCap+1))
		_ = f.Close()
		if readErr != nil || len(data) > bundleFileCap {
			return nil //nolint:nilerr // An unreadable or oversized file is excluded while other safe configuration can still transfer.
		}
		if total+int64(len(data)) > bundleTotalCap {
			return fmt.Errorf("config bundle exceeds %d bytes", bundleTotalCap)
		}
		entries++
		if entries > bundleEntryCap {
			return errors.New("config bundle has too many files")
		}
		hdr := &tar.Header{Name: rel, Mode: int64(info.Mode().Perm()), Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
		total += int64(len(data))
		return nil
	}

	addTree := func(root string) error {
		if !bundlePathWithoutLinks(homeRoot, root) {
			return nil
		}
		abs := filepath.Join(homeDir, root)
		info, err := homeRoot.Lstat(root)
		if err != nil {
			return nil //nolint:nilerr // a bundle root that does not exist is simply not bundled
		}
		if !info.IsDir() {
			return addFile(root, info)
		}
		return filepath.Walk(abs, func(p string, fi os.FileInfo, err error) error {
			if err != nil || !fi.Mode().IsRegular() {
				return nil //nolint:nilerr // unreadable entries, dirs, symlinks and sockets are skipped; symlinks would
				// bypass the per-file size cap (lstat size ≠ target size)
			}
			rel, err := filepath.Rel(homeDir, p)
			if err != nil {
				return nil //nolint:nilerr // a path that cannot be made relative to home is left out of the bundle
			}
			return addFile(filepath.ToSlash(rel), fi)
		})
	}

	for _, root := range bundleRoots {
		if err := addTree(root); err != nil {
			return nil, err
		}
	}

	// Plugins cache: ship only the highest version of each plugin.
	cache := filepath.Join(homeDir, ".claude/plugins/cache")
	if markets, err := os.ReadDir(cache); err == nil {
		for _, m := range markets {
			if !m.IsDir() {
				continue
			}
			plugins, err := os.ReadDir(filepath.Join(cache, m.Name()))
			if err != nil {
				continue
			}
			for _, p := range plugins {
				if !p.IsDir() {
					continue
				}
				versions, err := os.ReadDir(filepath.Join(cache, m.Name(), p.Name()))
				if err != nil {
					continue
				}
				var names []string
				for _, v := range versions {
					if v.IsDir() {
						names = append(names, v.Name())
					}
				}
				if len(names) == 0 {
					continue
				}
				sort.Slice(names, func(i, j int) bool { return compareVersionStrings(names[i], names[j]) < 0 })
				best := names[len(names)-1]
				rel := filepath.Join(".claude/plugins/cache", m.Name(), p.Name(), best)
				if err := addTree(rel); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func bundleRegularFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return info.Mode().IsRegular() && ok && stat.Nlink == 1
}

// Pin each parent before opening the next one. Separate path checks alone
// would permit a directory changed into an in-home symlink between checks.
func openBundleFile(root *os.Root, name string) (*os.File, error) {
	if !filepath.IsLocal(name) {
		return nil, errors.New("invalid bundle path")
	}
	parts := strings.Split(filepath.Clean(filepath.FromSlash(name)), string(filepath.Separator))
	current := root
	defer func() {
		if current != root {
			_ = current.Close()
		}
	}()
	for _, part := range parts[:len(parts)-1] {
		info, err := current.Lstat(part)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("invalid bundle parent")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			return nil, errors.New("bundle parent changed")
		}
		if current != root {
			_ = current.Close()
		}
		current = next
	}
	info, err := current.Lstat(parts[len(parts)-1])
	if err != nil || !bundleRegularFile(info) {
		return nil, errors.New("invalid bundle file")
	}
	f, err := current.Open(parts[len(parts)-1])
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !bundleRegularFile(opened) || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, errors.New("bundle file changed")
	}
	return f, nil
}

// The bundle contains selected configuration files, never the target of a
// convenient link into another part of a user's home or outside that home.
func bundlePathWithoutLinks(root *os.Root, name string) bool {
	clean := filepath.Clean(filepath.FromSlash(name))
	if !filepath.IsLocal(clean) {
		return false
	}
	prefix := ""
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		prefix = filepath.Join(prefix, part)
		info, err := root.Lstat(prefix)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}

// compareVersionStrings compares dotted versions numerically per component.
func compareVersionStrings(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var av, bv string
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		ai, bi := 0, 0
		aNum, bNum := true, true
		if _, err := fmt.Sscanf(av, "%d", &ai); err != nil {
			aNum = false
		}
		if _, err := fmt.Sscanf(bv, "%d", &bi); err != nil {
			bNum = false
		}
		if aNum && bNum {
			if ai != bi {
				if ai < bi {
					return -1
				}
				return 1
			}
			continue
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}

// UploadConfigBundle builds and POSTs the bundle to the server.
func UploadConfigBundle(ctx context.Context, serverHTTP, token, homeDir string) error {
	bundle, err := BuildConfigBundle(homeDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, bundleTransferTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverHTTP+"/api/agent-config", bytes.NewReader(bundle))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := (&http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("upload agent-config: HTTP %d", resp.StatusCode)
	}
	return nil
}

// DownloadConfigBundle fetches the bundle and unpacks it under destHome
// (creating destHome/.claude/...). Used by runner pods at startup.
func DownloadConfigBundle(ctx context.Context, serverHTTP, token, destHome string) error {
	ctx, cancel := context.WithTimeout(ctx, bundleTransferTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverHTTP+"/api/agent-config", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil // no bundle uploaded yet — run without user config
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download agent-config: HTTP %d", resp.StatusCode)
	}
	return ExtractConfigBundle(resp.Body, destHome)
}

// ExtractConfigBundle bounds decompression and replaces files through pinned
// directory handles. Existing links never redirect a write into credentials.
func ExtractConfigBundle(r io.Reader, destHome string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	limited := &io.LimitedReader{R: gz, N: bundleTotalCap + bundleEntryCap*2048 + 1}
	tr := tar.NewReader(limited)
	info, err := os.Lstat(destHome)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid bundle destination")
	}
	root, err := os.OpenRoot(destHome)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	openedRoot, err := root.Stat(".")
	if err != nil || !os.SameFile(info, openedRoot) {
		return errors.New("bundle destination changed")
	}
	var total int64
	for entries := 0; ; entries++ {
		hdr, err := tr.Next()
		if limited.N <= 0 {
			return errors.New("config bundle exceeds decompression limit")
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if entries >= bundleEntryCap {
			return errors.New("config bundle has too many entries")
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg || len(hdr.Name) > 4096 || strings.Count(filepath.Clean(filepath.FromSlash(hdr.Name)), string(filepath.Separator)) >= 128 || !filepath.IsLocal(filepath.FromSlash(hdr.Name)) || filepath.Clean(hdr.Name) == "." {
			return errors.New("invalid config bundle entry")
		}
		if hdr.Size < 0 || hdr.Size > bundleFileCap || total+hdr.Size > bundleTotalCap {
			return errors.New("config bundle exceeds size limit")
		}
		data, err := io.ReadAll(io.LimitReader(tr, bundleFileCap+1))
		if err != nil {
			return err
		}
		if int64(len(data)) != hdr.Size {
			return errors.New("invalid config bundle file size")
		}
		if err = replaceBundleFile(root, hdr.Name, data, os.FileMode(hdr.Mode&0700)|0600); err != nil {
			return err
		}
		total += int64(len(data))
	}
}

func replaceBundleFile(root *os.Root, name string, data []byte, mode os.FileMode) error {
	parts := strings.Split(filepath.Clean(filepath.FromSlash(name)), string(filepath.Separator))
	current := root
	defer func() {
		if current != root {
			_ = current.Close()
		}
	}()
	for _, part := range parts[:len(parts)-1] {
		if err := current.Mkdir(part, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := current.Lstat(part)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("invalid bundle destination parent")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			return err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			return errors.New("bundle destination parent changed")
		}
		if current != root {
			_ = current.Close()
		}
		current = next
	}
	leaf := parts[len(parts)-1]
	if info, err := current.Lstat(leaf); err == nil {
		if !bundleRegularFile(info) {
			return errors.New("invalid bundle destination file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	temporary := ".blerg-config-" + ccUUID() + ".tmp"
	f, err := current.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = current.Remove(temporary) }()
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Chmod(mode)
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return current.Rename(temporary, leaf)
}
