package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type credentialBuffer struct{ bytes.Buffer }

func (*credentialBuffer) Close() error { return nil }

func TestCodexManagedRefreshValidatesNativeAccountAndReason(t *testing.T) {
	for _, scenario := range []string{"valid", "foreign-account", "wrong-reason", "missing-account", "provider-failure", "account-changed", "no-source"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			source := func(_ context.Context, force bool, previous string) (EngineCredentialAccess, error) {
				calls++
				if !force || previous != "fixture-generation" {
					t.Error("refresh lost generation fence")
				}
				access := fixtureEngineAccess()
				access.Generation = "fixture-next-generation"
				if scenario == "account-changed" {
					access.AccountID = "other-provider-account"
				}
				if scenario == "provider-failure" {
					return EngineCredentialAccess{}, errors.New("fixture-private-provider-error")
				}
				return access, nil
			}
			d := &codexDriver{CredentialSource: source, managedCredential: codexCredentialState{account: "fixture-provider-account", generation: "fixture-generation"}}
			if scenario == "no-source" {
				d.CredentialSource = nil
			}
			sink := &credentialBuffer{}
			app := &codexApp{d: d, in: sink}
			request := map[string]string{"reason": "unauthorized", "previousAccountId": "fixture-provider-account"}
			switch scenario {
			case "foreign-account":
				request["previousAccountId"] = "foreign"
			case "wrong-reason":
				request["reason"] = "unknown"
			case "missing-account":
				delete(request, "previousAccountId")
			}
			params, _ := json.Marshal(request)
			app.refreshManagedCredential(context.Background(), codexRPC{ID: json.RawMessage(`42`), Params: params})
			if scenario == "valid" {
				if !strings.Contains(sink.String(), "fixture-private-access") || d.managedCredential.generation != "fixture-next-generation" {
					t.Fatal("refresh failed")
				}
			} else if strings.Contains(sink.String(), "fixture-private") || !strings.Contains(sink.String(), "error") {
				t.Fatal("invalid refresh disclosed credential")
			}
			if (scenario == "foreign-account" || scenario == "wrong-reason" || scenario == "missing-account" || scenario == "no-source") && calls != 0 {
				t.Fatal("unbound native request reached credential broker")
			}
		})
	}
}

func TestCodexManagedRefreshCoalescesWithoutHoldingLifecycleLock(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	source := func(context.Context, bool, string) (EngineCredentialAccess, error) {
		calls++
		close(entered)
		<-release
		access := fixtureEngineAccess()
		access.Generation = "next"
		return access, nil
	}
	d := &codexDriver{CredentialSource: source, managedCredential: codexCredentialState{account: "fixture-provider-account", generation: "fixture-generation"}}
	app := &codexApp{d: d}
	leader := make(chan error, 1)
	go func() {
		_, err := app.managedCredentialRefresh(context.Background(), "fixture-provider-account")
		leader <- err
	}()
	<-entered
	// A concurrent request must join the existing refresh rather than perform
	// provider I/O. Canceling that waiter must not cancel the leader's claim.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := app.managedCredentialRefresh(ctx, "fixture-provider-account"); err == nil {
		t.Fatal("canceled waiter succeeded")
	}
	d.managedCredential.mu.Lock()
	generation := d.managedCredential.generation
	d.managedCredential.mu.Unlock()
	if generation != "fixture-generation" {
		t.Fatal("pending refresh changed generation")
	}
	close(release)
	if err := <-leader; err != nil || calls != 1 {
		t.Fatal("concurrent unauthorized calls did not coalesce")
	}
}
