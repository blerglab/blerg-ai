package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// v0.5.4 predates BLERG_RUNNER_WEB_DIST and resolves frontend/dist from cwd.
// Supply that legacy layout inside this run's own directory, never the checkout.
func runnerWorkingDir(root, logs, dist string, historical bool) (string, error) {
	if !historical {
		return filepath.Join(root, "runner"), nil
	}
	dir := filepath.Join(logs, "runner-root")
	if err := os.MkdirAll(filepath.Join(dir, "frontend"), 0o700); err != nil {
		return "", err
	}
	if err := os.Symlink(dist, filepath.Join(dir, "frontend", "dist")); err != nil {
		return "", err
	}
	return dir, nil
}
func verifyRunnerEntry(ctx context.Context, origin, dist string) error {
	expected, err := os.ReadFile(filepath.Join(dist, "index.html")) //nolint:gosec // G304: the isolated build output selected by the orchestrator.
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/", nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	actual, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || !bytes.Equal(actual, expected) {
		return errors.New("runner_frontend_build_mismatch")
	}
	return nil
}
