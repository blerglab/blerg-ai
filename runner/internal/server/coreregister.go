package server

// StartCoreRegistration lets blerg-core act as the identity/discovery
// authority for blerg-runner: on startup (and roughly every heartbeat
// interval afterward) the server registers itself with blerg-core so core
// knows blerg-runner exists, where it lives, and what it can do.
//
// Both BLERG_CORE_URL and BLERG_CORE_REGISTER_KEY must be set for this to do
// anything — with either unset, StartCoreRegistration is a silent no-op so
// blerg-runner keeps working standalone.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
)

const (
	coreRegisterHeartbeat  = 60 * time.Second
	coreRegisterTimeout    = 10 * time.Second
	coreRegisterRetryFloor = 2 * time.Second
	coreRegisterRetryMax   = 30 * time.Second
)

// StartCoreRegistration registers blerg-runner with blerg-core and
// re-registers on a heartbeat interval. It blocks the calling goroutine
// (call it with `go`), retrying with backoff on failure, and never returns
// unless ctx is cancelled.
func StartCoreRegistration(ctx context.Context, coreURL, registerKey, selfURL, version string) {
	if coreURL == "" || registerKey == "" {
		log.Println("core registration disabled (BLERG_CORE_URL / BLERG_CORE_REGISTER_KEY not set)")
		return
	}
	if selfURL == "" {
		selfURL = "http://blerg-runner:8080"
	}

	// Register the full manifest entry, not just a name and a base URL: core's
	// aggregate GET /agents documents the runner from exactly this payload, so
	// anything missing here is missing from every agent's view of the install.
	// Core overwrites last_seen, stale and auth.token_endpoint, which are its to
	// know. Nothing in it is a secret.
	SetComponentVersion(version)
	body := RunnerManifest(selfURL)

	client := &http.Client{Timeout: coreRegisterTimeout}

	for {
		backoff := coreRegisterRetryFloor
		for {
			if err := registerOnce(ctx, client, coreURL, registerKey, body); err != nil {
				log.Printf("core registration: %v (retrying in %s)", err, backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				backoff *= 2
				if backoff > coreRegisterRetryMax {
					backoff = coreRegisterRetryMax
				}
				continue
			}
			log.Println("core registration: registered with blerg-core")
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(coreRegisterHeartbeat):
		}
	}
}

func registerOnce(ctx context.Context, client *http.Client, coreURL, registerKey string, body agentsmanifest.ComponentEntry) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coreURL+"/components", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+registerKey)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return &registerStatusError{status: resp.StatusCode}
	}
	return nil
}

type registerStatusError struct{ status int }

func (e *registerStatusError) Error() string {
	return "blerg-core returned " + http.StatusText(e.status)
}
