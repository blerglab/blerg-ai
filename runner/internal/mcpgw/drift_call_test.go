package mcpgw

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestStrictChangedCallIsRefusedNeutrallyAndRecorded(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	r := h.callTool(gh, "echo", map[string]any{"msg": "hi"})
	msg := r.errMessage()
	if !strings.Contains(msg, "do not ask the person") || strings.Contains(msg, "new session") {
		t.Fatalf("message = %q", msg)
	}
	if got := h.lastOutcome(gh); got != "changed" {
		t.Fatalf("outcome = %q, want changed", got)
	}
	// Recorded from the call alone: no list happened.
	rows, _ := db.ListOpenToolChanges(context.Background(), h.pool, "acct-1", gh.sessionID)
	if len(rows) != 1 || rows[0].Kind != db.ToolChangeChanged {
		t.Fatalf("changes = %+v", rows)
	}
	if _, err := db.GetToolDefinition(context.Background(), h.pool, h.up.hash("echo")); err != nil {
		t.Fatalf("definition not stored from the call path: %v", err)
	}
}

func TestDeclinedHashRevertsToTheNewSessionText(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	h.callTool(gh, "echo", map[string]any{})
	rows, _ := db.ListOpenToolChanges(context.Background(), h.pool, "acct-1", gh.sessionID)
	if _, err := db.DeclineToolChange(context.Background(), h.pool, "acct-1", rows[0].ID, "acct-1"); err != nil {
		t.Fatal(err)
	}
	// A fresh gateway state (a restart): the declined hash is blocked at the database.
	h2 := h.restart()
	r := h2.callTool(gh, "echo", map[string]any{})
	if msg := r.errMessage(); !strings.Contains(msg, "new session") {
		t.Fatalf("declined hash message = %q", msg)
	}
	// The same process, once its remembered outcome has aged out (one minute).
	h.clock.Advance(2 * time.Minute)
	r = h.callTool(gh, "echo", map[string]any{})
	if msg := r.errMessage(); !strings.Contains(msg, "new session") {
		t.Fatalf("declined hash message in the same process = %q", msg)
	}
}

func TestAutoAcceptCallRespectsThrottleAndUnreviewable(t *testing.T) {
	ref := &fakeRefresher{}
	h := newHarness(t, func(c *Config) { c.Refresher = ref })
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "reads \u202E things")
	h.clock.Advance(time.Minute)
	r := h.callTool(gh, "echo", map[string]any{})
	if msg := r.errMessage(); !strings.Contains(msg, "do not ask the person") || ref.count() != 0 {
		t.Fatalf("unreviewable under auto_accept: msg=%q refreshes=%d", msg, ref.count())
	}
}

func TestAutoAcceptCallIsStrictOnceThrottled(t *testing.T) {
	ref := &fakeRefresher{}
	h := newHarness(t, func(c *Config) { c.Refresher = ref })
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	for i := 1; i <= 3; i++ {
		h.up.setDescription("echo", "v"+string(rune('0'+i)))
		h.clock.Advance(time.Minute)
		if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 1 {
			t.Fatalf("re-pin %d refused: %v", i, got)
		}
	}
	before := ref.count() // the list re-pins above may have asked for refreshes themselves
	h.up.setDescription("echo", "v4")
	h.clock.Advance(time.Minute)
	r := h.callTool(gh, "echo", map[string]any{})
	if msg := r.errMessage(); !strings.Contains(msg, "do not ask the person") || ref.count() != before {
		t.Fatalf("throttled under auto_accept: msg=%q refreshes=%d (was %d)", msg, ref.count(), before)
	}
	if got := h.lastOutcome(gh); got != "changed" {
		t.Fatalf("outcome = %q, want changed", got)
	}
}

func TestAutoAcceptCallNeverRelistsADeclinedHash(t *testing.T) {
	ref := &fakeRefresher{}
	h := newHarness(t, func(c *Config) { c.Refresher = ref })
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	old := h.grantRow(gh).Tools["echo"].Hash
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	h.callTool(gh, "echo", map[string]any{}) // strict: recorded
	rows, _ := db.ListOpenToolChanges(context.Background(), h.pool, "acct-1", gh.sessionID)
	if len(rows) != 1 {
		t.Fatalf("changes = %+v", rows)
	}
	if _, err := db.DeclineToolChangeWithEvent(context.Background(), h.pool, "acct-1", rows[0].ID, "acct-1"); err != nil {
		t.Fatal(err)
	}
	h.core.setPolicy(PinPolicyAutoAccept)
	h.clock.Advance(6 * time.Minute)
	r := h.callTool(gh, "echo", map[string]any{})
	if msg := r.errMessage(); !strings.Contains(msg, "new session") || ref.count() != 0 {
		t.Fatalf("declined hash under auto_accept: msg=%q refreshes=%d", msg, ref.count())
	}
	if got := h.lastOutcome(gh); got != "changed" {
		t.Fatalf("outcome = %q, want changed", got)
	}
	if h.grantRow(gh).Tools["echo"].Hash != old {
		t.Fatal("a call must never re-pin")
	}
}

func TestAutoAcceptCallBurstAgainstAnUnreviewableToolStaysStrict(t *testing.T) {
	ref := &fakeRefresher{}
	h := newHarness(t, func(c *Config) { c.Refresher = ref })
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "reads \u202E things")
	h.clock.Advance(time.Minute)
	for i := 1; i <= 2; i++ {
		r := h.callTool(gh, "echo", map[string]any{})
		if msg := r.errMessage(); !strings.Contains(msg, "do not ask the person") || ref.count() != 0 {
			t.Fatalf("call %d: msg=%q refreshes=%d", i, msg, ref.count())
		}
	}
}

// The unsafe classes the gateway writes are the database's constants.
func TestUnsafeClassesMatchDB(t *testing.T) {
	big, _ := CanonicalTool("t", strings.Repeat("x", MaxDefinitionBytes+1), nil)
	nul, _ := CanonicalTool("t", "a\x00b", nil)
	bidi, _ := CanonicalTool("t", "a\u202Eb", nil)
	if ClassifyDefinition(big) != db.UnsafeTooLarge || ClassifyDefinition(nul) != db.UnsafeNUL || ClassifyDefinition(bidi) != db.UnsafeHiddenChars {
		t.Fatal("ClassifyDefinition and the db constants disagree")
	}
}

func TestAutoAcceptCallIsRefusedUntilListedAndAsksForARefresh(t *testing.T) {
	ref := &fakeRefresher{}
	h := newHarness(t, func(c *Config) { c.Refresher = ref })
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	r := h.callTool(gh, "echo", map[string]any{})
	if msg := r.errMessage(); !strings.Contains(msg, "list tools again") {
		t.Fatalf("message = %q", msg)
	}
	if h.lastOutcome(gh) != "changed" || ref.count() != 1 {
		t.Fatalf("outcome=%s refreshes=%d", h.lastOutcome(gh), ref.count())
	}
	// The grant is untouched by a call: it still holds the old hash, not the live one.
	if h.grantRow(gh).Tools["echo"].Hash == h.up.hash("echo") {
		t.Fatal("a call must never re-pin")
	}
	// A second call within the minute does not ask again.
	h.callTool(gh, "echo", map[string]any{})
	if ref.count() != 1 {
		t.Fatalf("refreshes = %d, want 1 within a minute", ref.count())
	}
	// After the list the call goes through.
	h.call(gh, "tools/list", map[string]any{})
	if r := h.callTool(gh, "echo", map[string]any{"msg": "hi"}); r.errMessage() != "" {
		t.Fatalf("call after list: %s", r.raw)
	}
}

func TestReenabledEntryIsRefusedUntilListed(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	old := h.up.hash("echo")
	h.up.setDescription("echo", "v2")
	// An accept (the review route) re-pins directly.
	if ok, err := db.RepinGrantTool(context.Background(), h.pool, gh.sessionID, gh.connectionID, "echo", old, h.up.hash("echo"), ""); err != nil || !ok {
		t.Fatalf("repin: %v %v", ok, err)
	}
	h.clock.Advance(time.Minute)
	r := h.callTool(gh, "echo", map[string]any{})
	if msg := r.errMessage(); !strings.Contains(msg, "re-enabled") || h.lastOutcome(gh) != "changed" {
		t.Fatalf("message = %q outcome=%s", msg, h.lastOutcome(gh))
	}
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 1 {
		t.Fatalf("list after accept: %v", got)
	}
	if r := h.callTool(gh, "echo", map[string]any{"msg": "hi"}); r.errMessage() != "" {
		t.Fatalf("call after list: %s", r.raw)
	}
}
