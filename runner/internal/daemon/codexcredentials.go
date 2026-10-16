package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

type codexCredentialFlight struct {
	done   chan struct{}
	access EngineCredentialAccess
	err    error
}
type codexCredentialState struct {
	mu         sync.Mutex
	account    string
	generation string
	refreshing *codexCredentialFlight
}

func (a *codexApp) loginManagedCredential(parent context.Context) error {
	if a.d.CredentialSource == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	access, err := a.d.CredentialSource(ctx, false, "")
	if err != nil || !validCodexCredential(access) || ctx.Err() != nil {
		return errEngineCredential
	}
	_, err = a.callContext(ctx, "account/login/start", map[string]any{"type": "chatgptAuthTokens", "accessToken": access.AccessToken, "chatgptAccountId": access.AccountID, "chatgptPlanType": access.PlanType})
	if err != nil {
		return errEngineCredential
	}
	state := &a.d.managedCredential
	state.mu.Lock()
	defer state.mu.Unlock()
	state.account = access.AccountID
	state.generation = access.Generation
	state.refreshing = nil
	return nil
}

func validCodexCredential(access EngineCredentialAccess) bool {
	return access.Mode == "chatgptAuthTokens" && access.AccessToken != "" && access.AccountID != "" && access.Generation != "" && access.ExpiresAt.After(time.Now())
}

// Concurrent unauthorized requests share one refresh and one generation fence.
// Provider I/O never holds the state lock or the native lifecycle lock.
func (a *codexApp) managedCredentialRefresh(ctx context.Context, account string) (EngineCredentialAccess, error) {
	state := &a.d.managedCredential
	state.mu.Lock()
	if ctx.Err() != nil || account != state.account || state.generation == "" {
		state.mu.Unlock()
		return EngineCredentialAccess{}, errEngineCredential
	}
	if flight := state.refreshing; flight != nil {
		state.mu.Unlock()
		select {
		case <-flight.done:
			if ctx.Err() != nil {
				return EngineCredentialAccess{}, errEngineCredential
			}
			return flight.access, flight.err
		case <-ctx.Done():
			return EngineCredentialAccess{}, errEngineCredential
		}
	}
	previous := state.generation
	flight := &codexCredentialFlight{done: make(chan struct{})}
	state.refreshing = flight
	state.mu.Unlock()
	access, err := a.d.CredentialSource(ctx, true, previous)
	state.mu.Lock()
	defer state.mu.Unlock()
	if err != nil || ctx.Err() != nil || !validCodexCredential(access) || access.AccountID != account || state.refreshing != flight || state.generation != previous || state.account != account {
		flight.err = errEngineCredential
	} else {
		state.generation = access.Generation
		flight.access = access
	}
	if state.refreshing == flight {
		state.refreshing = nil
	}
	close(flight.done)
	return flight.access, flight.err
}

func (a *codexApp) refreshManagedCredential(parent context.Context, ev codexRPC) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	refuse := func() {
		_ = a.writeContext(ctx, map[string]any{"id": ev.ID, "error": map[string]any{"code": -32001, "message": "Managed connection unavailable; reconnect in Settings"}})
	}
	var request struct {
		Reason            string `json:"reason"`
		PreviousAccountID string `json:"previousAccountId"`
	}
	if a.d.CredentialSource == nil || len(ev.ID) == 0 || len(ev.ID) > 256 || json.Unmarshal(ev.Params, &request) != nil || request.Reason != "unauthorized" || request.PreviousAccountID == "" {
		refuse()
		return
	}
	access, err := a.managedCredentialRefresh(ctx, request.PreviousAccountID)
	if err != nil || ctx.Err() != nil {
		refuse()
		return
	}
	_ = a.writeContext(ctx, map[string]any{"id": ev.ID, "result": map[string]any{"accessToken": access.AccessToken, "chatgptAccountId": access.AccountID, "chatgptPlanType": access.PlanType}})
}
