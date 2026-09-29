package coreauth

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
)

const (
	registerRetryInterval     = 15 * time.Second
	registerHeartbeatInterval = 60 * time.Second
	registerTimeout           = 10 * time.Second
)

// StartRegistration launches a background goroutine that registers
// blerg-board with blerg-core (POST /components, Bearer registerKey) and
// keeps re-registering as a heartbeat. It retries every ~15s until the first
// success, then re-registers roughly every 60s for as long as ctx is alive.
//
// entry is the component's full manifest entry (api.BoardManifest): core's
// aggregate GET /agents documents blerg-board from exactly this payload, so
// anything missing here is missing from every agent's view of the install.
// Core overwrites last_seen, stale and auth.token_endpoint, which are its to
// know. Nothing in it is a secret.
//
// A no-op (skip silently) unless both coreURL and registerKey are set, so
// blerg-board degrades to standalone when the control plane isn't
// configured.
func StartRegistration(ctx context.Context, coreURL, registerKey string, entry agentsmanifest.ComponentEntry) {
	if coreURL == "" || registerKey == "" {
		return
	}
	client := &http.Client{Timeout: registerTimeout}

	register := func() bool {
		if err := registerOnce(ctx, client, coreURL, registerKey, entry); err != nil {
			log.Printf("coreauth: register with blerg-core failed: %v", err)
			return false
		}
		return true
	}

	go func() {
		for {
			if register() {
				log.Printf("coreauth: registered blerg-board with blerg-core at %s", coreURL)
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(registerRetryInterval):
			}
		}

		t := time.NewTicker(registerHeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !register() {
					log.Printf("coreauth: heartbeat re-register with blerg-core failed, will retry next tick")
				}
			}
		}
	}()
}

// registerOnce POSTs the manifest entry to core's component directory once.
func registerOnce(ctx context.Context, client *http.Client, coreURL, registerKey string, entry agentsmanifest.ComponentEntry) error {
	payload, err := json.Marshal(entry)
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
	return "blerg-core returned HTTP " + strconv.Itoa(e.status) + " " + http.StatusText(e.status)
}
