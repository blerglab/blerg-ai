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
	"time"
)

// Config-bundle limits: pods have no ~/.claude or ~/.hermes, so the daemon
// ships a synced copy of the user's agent config (CLAUDE.md, skills, agent
// defs, one version per cached plugin, hermes's model/provider choice).
// Oversized files are skipped, the whole bundle is capped.
const (
	bundleFileCap  = 5 << 20  // per-file
	bundleTotalCap = 64 << 20 // whole bundle
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
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	total := int64(0)

	addFile := func(abs, rel string, info os.FileInfo) error {
		if info.Size() > bundleFileCap {
			return nil // skip oversized files
		}
		if total+info.Size() > bundleTotalCap {
			return fmt.Errorf("config bundle exceeds %d bytes", bundleTotalCap)
		}
		data, err := os.ReadFile(abs) //nolint:gosec // abs comes from Walk over the fixed bundleRoots under home; regular files only, size checked against bundleFileCap first
		if err != nil {
			return nil //nolint:nilerr // an unreadable file is left out of the bundle, the rest still ships
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
		abs := filepath.Join(homeDir, root)
		info, err := os.Stat(abs)
		if err != nil {
			return nil //nolint:nilerr // a bundle root that does not exist is simply not bundled
		}
		if !info.IsDir() {
			return addFile(abs, root, info)
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
			return addFile(p, filepath.ToSlash(rel), fi)
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("upload agent-config: %d: %s", resp.StatusCode, raw)
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil // no bundle uploaded yet — run without user config
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("download agent-config: %d: %s", resp.StatusCode, raw)
	}
	return ExtractConfigBundle(resp.Body, destHome)
}

// ExtractConfigBundle unpacks a bundle stream under destHome, rejecting path
// escapes.
func ExtractConfigBundle(r io.Reader, destHome string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	root, err := filepath.Abs(destHome)
	if err != nil {
		return err
	}
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
		dest := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if !strings.HasPrefix(dest, root+string(filepath.Separator)) {
			return fmt.Errorf("bundle path escapes destination: %s", hdr.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(tr, bundleFileCap+1))
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, os.FileMode(hdr.Mode&0o777)|0o400); err != nil {
			return err
		}
	}
}
