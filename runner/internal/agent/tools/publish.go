package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// PublishConfig points the publishing tools at the blerg-runner server.
type PublishConfig struct {
	Base  string // server HTTP base
	Token string // daemon token
}

const maxPublishUpload = 20 << 20

// readFileMax reads a whole file but refuses to hold more than limit bytes in
// memory: an oversized file is rejected after limit+1 bytes, not after being
// loaded entirely.
func readFileMax(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p) //nolint:gosec // p is validated by resolve() (or comes from a Walk under a validated root) to stay inside the workspace
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errFileTooLarge
	}
	return data, nil
}

const maxScreenshotUpload = 8 << 20

var errFileTooLarge = errors.New("file too large")

func publishClient() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

func postPublish(ctx context.Context, cfg PublishConfig, path, contentType string, body io.Reader) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Base+path, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", contentType)
	resp, err := publishClient().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("publish: %d: %s", resp.StatusCode, raw)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.URL, nil
}

// PushMockup returns the "push_mockup" tool: tars a workspace directory of
// static files and publishes it; the result is the immutable served URL.
func PushMockup(workDir string, cfg PublishConfig) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "push_mockup",
			Description: "Publish a directory of static HTML/CSS/JS from the workspace as a viewable mockup. Returns the URL (open it from any device). Re-pushing returns a NEW url — mockups are immutable.",
			InputSchema: schema(`{"type":"object","properties":{"dir":{"type":"string","description":"workspace-relative directory containing index.html"},"title":{"type":"string"}},"required":["dir"]}`),
		},
		mutating: true, // external publish
		run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var args struct{ Dir, Title string }
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			root, err := resolve(workDir, args.Dir)
			if err != nil {
				return "", err
			}
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			total := int64(0)
			err = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					return err
				}
				// Only regular files: Walk does not follow symlinks, but
				// reading one would, and a link inside the mockup directory
				// must not publish a file from outside the workspace.
				if !fi.Mode().IsRegular() {
					return nil
				}
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				data, err := readFileMax(p, maxPublishUpload-total)
				if errors.Is(err, errFileTooLarge) {
					return fmt.Errorf("mockup exceeds %dMB", maxPublishUpload>>20)
				}
				if err != nil {
					return err
				}
				total += int64(len(data))
				if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(data))}); err != nil {
					return err
				}
				_, err = tw.Write(data)
				return err
			})
			if err != nil {
				return "", err
			}
			if err := tw.Close(); err != nil {
				return "", err
			}
			if err := gz.Close(); err != nil {
				return "", err
			}
			url, err := postPublish(ctx, cfg, "/api/mockups", "application/gzip", &buf)
			if err != nil {
				return "", err
			}
			return "Mockup published: " + url, nil
		},
	}
}

// PushScreenshot returns the "push_screenshot" tool: publishes a PNG/JPEG
// from the workspace (e.g. captured with headless chromium).
func PushScreenshot(workDir string, cfg PublishConfig) agent.Tool {
	return fnTool{
		def: agent.ToolDef{
			Name:        "push_screenshot",
			Description: "Publish a PNG/JPEG screenshot from the workspace so the user can view it. Capture with e.g.: chromium --headless --screenshot=shot.png --window-size=1280,800 <url-or-file>. Returns the image URL.",
			InputSchema: schema(`{"type":"object","properties":{"file":{"type":"string","description":"workspace-relative image path"},"caption":{"type":"string"}},"required":["file"]}`),
		},
		mutating: true, // external publish
		run: func(ctx context.Context, in json.RawMessage) (string, error) {
			var args struct{ File, Caption string }
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			p, err := resolve(workDir, args.File)
			if err != nil {
				return "", err
			}
			data, err := readFileMax(p, maxScreenshotUpload)
			if errors.Is(err, errFileTooLarge) {
				return "", fmt.Errorf("screenshot exceeds 8MB")
			}
			if err != nil {
				return "", err
			}
			url, err := postPublish(ctx, cfg, "/api/screenshots", "application/octet-stream", bytes.NewReader(data))
			if err != nil {
				return "", err
			}
			return "Screenshot published: " + url, nil
		},
	}
}
