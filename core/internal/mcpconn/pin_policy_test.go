package mcpconn_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

func TestPinPolicyDefaultsToStrictAndPatches(t *testing.T) {
	svc, pool := newSvc(t)
	acct := newAccount(t, pool)
	ctx := context.Background()
	c, err := svc.Create(ctx, acct, mcpconn.CreateInput{Name: "cal", URL: goodURL, AuthKind: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if c.PinPolicy != "strict" {
		t.Fatalf("default pin_policy = %q", c.PinPolicy)
	}
	auto := "auto_accept"
	c, err = svc.Patch(ctx, acct, c.ID, mcpconn.PatchInput{PinPolicy: &auto})
	if err != nil || c.PinPolicy != "auto_accept" {
		t.Fatalf("patch: %+v err=%v", c, err)
	}
	bad := "trust-everything"
	if _, err := svc.Patch(ctx, acct, c.ID, mcpconn.PatchInput{PinPolicy: &bad}); err == nil {
		t.Fatal("an unknown policy must be refused")
	}
	// The flip is audited: from strict to auto_accept.
	evs, err := svc.ListConnectionEvents(ctx, acct, c.ID, 10)
	if err != nil || len(evs) != 1 || evs[0].Action != "pin_policy" || evs[0].FromValue != "strict" || evs[0].ToValue != "auto_accept" {
		t.Fatalf("events: %+v err=%v", evs, err)
	}
	// Patching to the same value writes no event.
	if _, err := svc.Patch(ctx, acct, c.ID, mcpconn.PatchInput{PinPolicy: &auto}); err != nil {
		t.Fatal(err)
	}
	if evs, _ = svc.ListConnectionEvents(ctx, acct, c.ID, 10); len(evs) != 1 {
		t.Fatalf("a no-op flip must not be audited: %d", len(evs))
	}
	// Another account sees nothing.
	if evs, _ := svc.ListConnectionEvents(ctx, "00000000-0000-4000-8000-0000000000ff", c.ID, 10); len(evs) != 0 {
		t.Fatal("events leaked across accounts")
	}
	// The credential hand-out carries the policy.
	res, err := svc.FetchSecret(ctx, acct, c.ID, mcpconn.Proof{SessionID: sessA})
	if err != nil || res.PinPolicy != "auto_accept" {
		t.Fatalf("FetchSecret: %+v err=%v", res, err)
	}
}
