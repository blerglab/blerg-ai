package mcpgw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func openChanges(t *testing.T, h *harness, gh grantHandle) []db.ToolChange {
	t.Helper()
	rows, err := db.ListOpenToolChanges(context.Background(), h.pool, "acct-1", gh.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func events(t *testing.T, h *harness, gh grantHandle) []db.PinEvent {
	t.Helper()
	evs, err := db.ListPinEventsForSession(context.Background(), h.pool, "acct-1", gh.sessionID, 50)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestStrictChangedToolIsHiddenRecordedOnceAndDefinitionStored(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	old := h.up.hash("echo")
	h.up.setDescription("echo", "now does something else")
	h.clock.Advance(time.Minute)
	for i := 0; i < 10; i++ {
		if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 0 {
			t.Fatalf("list %d still shows the changed tool: %v", i, got)
		}
	}
	rows := openChanges(t, h, gh)
	if len(rows) != 1 || rows[0].Kind != db.ToolChangeChanged || rows[0].OldHash != old || rows[0].NewHash != h.up.hash("echo") || rows[0].Mode != "allow" {
		t.Fatalf("changes = %+v", rows)
	}
	d, err := db.GetToolDefinition(context.Background(), h.pool, h.up.hash("echo"))
	if err != nil || !strings.Contains(d.Canonical, "now does something else") || d.Unsafe != "" {
		t.Fatalf("definition = %+v err=%v", d, err)
	}
	if n := len(events(t, h, gh)); n != 0 {
		t.Fatalf("strict wrote %d pin events", n)
	}
}

func TestReturnToThePinSupersedesThePendingChange(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	h.call(gh, "tools/list", map[string]any{})
	if len(openChanges(t, h, gh)) != 1 {
		t.Fatal("no change recorded")
	}
	h.up.setDescription("echo", "echoes")
	h.clock.Advance(time.Minute)
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 1 {
		t.Fatalf("restored tool not listed: %v", got)
	}
	if len(openChanges(t, h, gh)) != 0 {
		t.Fatal("pending change not superseded after the pin matched again")
	}
}

// Review Focus 2: a tool the person left off is not new.
func TestNewToolIsRecordedOnlyWhenTheLaunchNeverSawIt(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetAllowAll, KnownTools: h.known()})
	h.call(gh, "tools/list", map[string]any{})
	if n := len(openChanges(t, h, gh)); n != 0 {
		t.Fatalf("tools left off at launch were recorded as new: %d", n)
	}
	if r := h.callTool(gh, "secret", map[string]any{}); r.errMessage() != "unknown tool" {
		t.Fatalf("left-off tool: %q", r.errMessage())
	}
	h.up.addTool(fakeTool{Name: "extra", Description: "new on the server", Schema: `{"type":"object"}`})
	h.clock.Advance(time.Minute)
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 1 {
		t.Fatalf("a new tool must stay hidden under strict: %v", got)
	}
	rows := openChanges(t, h, gh)
	if len(rows) != 1 || rows[0].Kind != db.ToolChangeNew || rows[0].Tool != "extra" || rows[0].Mode != "allow" {
		t.Fatalf("changes = %+v", rows)
	}
	// A grant whose launch listing is unknown never records new.
	gh2 := h.grantSpec("beta", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"})})
	h.call(gh2, "tools/list", map[string]any{})
	if n := len(openChanges(t, h, gh2)); n != 0 {
		t.Fatalf("NULL known_tools recorded %d new tools", n)
	}
}

func TestAutoAcceptRepinsOnListAndMarksListed(t *testing.T) {
	ref := &fakeRefresher{}
	h := newHarness(t, func(c *Config) { c.Refresher = ref })
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	old := h.up.hash("echo")
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	r := h.call(gh, "tools/list", map[string]any{})
	if got := toolNames(r); len(got) != 1 || got[0] != "echo" {
		t.Fatalf("auto_accept must serve the re-pinned tool: %v", got)
	}
	gr := h.grantRow(gh)
	if e := gr.Tools["echo"]; e.Hash != h.up.hash("echo") || !e.IsListed() {
		t.Fatalf("after list: %+v (want the new hash, listed)", e)
	}
	evs := events(t, h, gh)
	if len(evs) != 1 || evs[0].Action != db.PinRepinned || evs[0].OldHash != old || evs[0].Actor != "policy:auto_accept" {
		t.Fatalf("events = %+v", evs)
	}
	if r := h.callTool(gh, "echo", map[string]any{"msg": "hi"}); r.errMessage() != "" {
		t.Fatalf("call after re-pin and list: %s", r.raw)
	}
	if ref.count() != 0 {
		t.Fatalf("refresh requests = %d, want 0 (the list that re-pinned carries the new text)", ref.count())
	}
	if n := len(openChanges(t, h, gh)); n != 0 {
		t.Fatalf("auto_accept opened %d change rows", n)
	}
}

// Review Focus 3: a strict change pending, then the policy flips to auto_accept.
func TestPolicyFlipToAutoAcceptResolvesAPendingChange(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	h.call(gh, "tools/list", map[string]any{})
	if len(openChanges(t, h, gh)) != 1 {
		t.Fatal("no pending change")
	}
	h.core.setPolicy(PinPolicyAutoAccept)
	h.clock.Advance(6 * time.Minute) // past the credential cache
	// A restart in between: the supersede must not depend on this process's memory.
	h2 := h.restart()
	if got := toolNames(h2.call(gh, "tools/list", map[string]any{})); len(got) != 1 {
		t.Fatalf("not re-pinned after the flip: %v", got)
	}
	if len(openChanges(t, h, gh)) != 0 {
		t.Fatal("the pending change survived the re-pin")
	}
}

func TestRestartedGatewaySupersedesWhenThePinMatchesAgain(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	h.call(gh, "tools/list", map[string]any{})
	h.up.setDescription("echo", "echoes")
	h.clock.Advance(time.Minute)
	h2 := h.restart()
	h2.callTool(gh, "echo", map[string]any{"msg": "hi"}) // a call alone, no list
	if len(openChanges(t, h, gh)) != 0 {
		t.Fatal("a restarted gateway did not supersede the row when the pin matched again")
	}
}

// Review Focus 1: a list that served B must not mark C listed after a re-pin landed in between.
func TestMarkListedAfterARacingRepinLeavesTheEntryUnlisted(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	ctx := context.Background()
	gr := h.grantRow(gh)
	st := h.gw.stateFor(ctx, gr)
	tools, complete, _, err := st.listUpstream(ctx, gr, maxPages, false)
	if err != nil {
		t.Fatal(err)
	}
	_, listed := h.gw.reconcileListing(ctx, gr, st, tools, complete)
	hashB := gr.Tools["echo"].Hash
	// An accept lands before markListed: the entry now pins C, unlisted.
	if ok, err := db.RepinGrantTool(ctx, h.pool, gh.sessionID, gh.connectionID, "echo", hashB, "hash-c", ""); err != nil || !ok {
		t.Fatalf("repin: %v %v", ok, err)
	}
	listed["echo"] = hashB // what the list served
	h.gw.markListed(ctx, gr, listed)
	if h.grantRow(gh).Tools["echo"].IsListed() {
		t.Fatal("a list that served B marked C as listed")
	}
}

func TestAutoAcceptGrantsNewToolsByPreset(t *testing.T) {
	for _, tc := range []struct{ preset, wantMode string }{
		{db.PresetAllowAll, ModeAllow}, {db.PresetApproval, ModePropose}, {db.PresetCustom, ""},
	} {
		h := newHarness(t, func(c *Config) { c.Sink = &fakeSink{} })
		h.core.setPolicy(PinPolicyAutoAccept)
		gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: tc.preset, KnownTools: h.known()})
		h.up.addTool(fakeTool{Name: "extra", Description: "new", Schema: `{"type":"object"}`})
		h.clock.Advance(time.Minute)
		got := toolNames(h.call(gh, "tools/list", map[string]any{}))
		gr := h.grantRow(gh)
		e, granted := gr.Tools["extra"]
		switch {
		case tc.wantMode == "" && (granted || len(got) != 1 || len(openChanges(t, h, gh)) != 1):
			t.Fatalf("%s: custom must record, not grant: granted=%v listed=%v", tc.preset, granted, got)
		case tc.wantMode != "" && (!granted || e.Mode != tc.wantMode || len(got) != 2):
			t.Fatalf("%s: granted=%v mode=%q listed=%v", tc.preset, granted, e.Mode, got)
		}
		if tc.wantMode != "" {
			evs := events(t, h, gh)
			if len(evs) != 1 || evs[0].Action != db.PinGranted || evs[0].Mode != tc.wantMode {
				t.Fatalf("%s: events = %+v", tc.preset, evs)
			}
		}
	}
}

func TestAutoAcceptIsThrottledAfterThreeRepinsPerHour(t *testing.T) {
	h := newHarness(t, nil)
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	for i := 1; i <= 3; i++ {
		h.up.setDescription("echo", "v"+string(rune('0'+i)))
		h.clock.Advance(time.Minute)
		if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 1 {
			t.Fatalf("re-pin %d refused: %v", i, got)
		}
	}
	h.up.setDescription("echo", "v4")
	h.clock.Advance(time.Minute)
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 0 {
		t.Fatalf("fourth re-pin within the hour must be refused: %v", got)
	}
	rows := openChanges(t, h, gh)
	if len(rows) != 1 || rows[0].Kind != db.ToolChangeChanged {
		t.Fatalf("throttled drift must be recorded for review: %+v", rows)
	}
	var throttled int
	for _, e := range events(t, h, gh) {
		if e.Action == db.PinThrottled {
			throttled++
		}
	}
	if throttled != 1 {
		t.Fatalf("throttled events = %d, want 1", throttled)
	}
}

func TestDuplicateNameIsOffAndNeverRecorded(t *testing.T) {
	h := newHarness(t, nil)
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetAllowAll, KnownTools: h.known()})
	h.up.addTool(fakeTool{Name: "echo", Description: "impostor", Schema: `{"type":"object"}`})
	h.clock.Advance(time.Minute)
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 0 {
		t.Fatalf("a duplicated name must be off: %v", got)
	}
	if n := len(openChanges(t, h, gh)) + len(events(t, h, gh)); n != 0 {
		t.Fatalf("a duplicated name wrote %d rows", n)
	}
}

func TestRemovedToolIsRecordedFromACompleteListingAndClosesOnReturn(t *testing.T) {
	h := newHarness(t, nil)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.removeTool("echo")
	h.clock.Advance(time.Minute)
	h.call(gh, "tools/list", map[string]any{})
	rows := openChanges(t, h, gh)
	if len(rows) != 1 || rows[0].Kind != db.ToolChangeRemoved || rows[0].NewHash != "" {
		t.Fatalf("changes = %+v", rows)
	}
	h.up.addTool(fakeTool{Name: "echo", Description: "echoes", Schema: `{"type":"object","properties":{"msg":{"type":"string"}}}`})
	h.clock.Advance(time.Minute)
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 1 {
		t.Fatalf("returned tool not listed: %v", got)
	}
	if len(openChanges(t, h, gh)) != 0 {
		t.Fatal("removed row not superseded on return")
	}
}

func TestUnreviewableDefinitionIsNeverRepinned(t *testing.T) {
	h := newHarness(t, nil)
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.setDescription("echo", "reads a file \u202E and more")
	h.clock.Advance(time.Minute)
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 0 {
		t.Fatalf("an unreviewable definition was served: %v", got)
	}
	d, err := db.GetToolDefinition(context.Background(), h.pool, h.up.hash("echo"))
	if err != nil || d.Unsafe != db.UnsafeHiddenChars {
		t.Fatalf("definition = %+v err=%v", d, err)
	}
	if len(openChanges(t, h, gh)) != 1 || len(events(t, h, gh)) != 0 {
		t.Fatal("unreviewable drift must be recorded for the person and never re-pinned")
	}
}

func TestChangeCapKeepsTheListWorking(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.ChangeLimits = db.ToolChangeLimits{Session: 1, Account: 10} })
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.addTool(fakeTool{Name: "x1", Description: "n", Schema: `{"type":"object"}`})
	h.up.addTool(fakeTool{Name: "x2", Description: "n", Schema: `{"type":"object"}`})
	h.clock.Advance(time.Minute)
	r := h.call(gh, "tools/list", map[string]any{})
	if r.errMessage() != "" || len(toolNames(r)) != 1 {
		t.Fatalf("list under the cap: %s", r.raw)
	}
	if n := len(openChanges(t, h, gh)); n != 1 {
		t.Fatalf("recorded %d, want the cap of 1", n)
	}
	// The capped outcome is remembered: more lists within the minute do not ask the database again.
	for i := 0; i < 5; i++ {
		if r := h.call(gh, "tools/list", map[string]any{}); r.errMessage() != "" || len(toolNames(r)) != 1 {
			t.Fatalf("repeat list %d: %s", i, r.raw)
		}
	}
	if n := len(openChanges(t, h, gh)); n != 1 {
		t.Fatalf("recorded %d after repeats, want 1", n)
	}
	ctx := context.Background()
	st := h.gw.stateFor(ctx, h.grantRow(gh))
	capped := 0
	for _, name := range []string{"x1", "x2"} {
		if out, ok := st.recordedOutcome(driftKey(db.ToolChangeNew, name, h.up.hash(name)), h.clock.Now()); ok && out.Capped {
			capped++
		}
	}
	if capped != 1 {
		t.Fatalf("remembered capped outcomes = %d, want 1 (the tool the cap turned away)", capped)
	}
}

func TestRevertedHashHasAStoredDefinition(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	hashA := h.up.hash("echo")
	// A is classified on the pinned-match path (never stored there).
	h.call(gh, "tools/list", map[string]any{})
	h.up.setDescription("echo", "B")
	hashB := h.up.hash("echo")
	h.clock.Advance(time.Minute)
	h.call(gh, "tools/list", map[string]any{})
	if ok, err := db.RepinGrantTool(ctx, h.pool, gh.sessionID, gh.connectionID, "echo", hashA, hashB, ""); err != nil || !ok {
		t.Fatalf("repin: %v %v", ok, err)
	}
	if _, err := db.MarkListed(ctx, h.pool, gh.sessionID, gh.connectionID, "echo", hashB); err != nil {
		t.Fatal(err)
	}
	h.up.setDescription("echo", "echoes")
	if h.up.hash("echo") != hashA {
		t.Fatal("the original description no longer hashes to A")
	}
	h.clock.Advance(time.Minute)
	h.call(gh, "tools/list", map[string]any{})
	rows := openChanges(t, h, gh)
	if len(rows) != 1 || rows[0].NewHash != hashA {
		t.Fatalf("changes = %+v, want one pending change to A", rows)
	}
	if _, err := db.GetToolDefinition(ctx, h.pool, hashA); err != nil {
		t.Fatalf("the reverted-to definition was never stored: %v", err)
	}
}

func TestAutoAcceptNeverRepinsADeclinedHash(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	hashA := h.up.hash("echo")
	h.up.setDescription("echo", "B")
	h.clock.Advance(time.Minute)
	h.call(gh, "tools/list", map[string]any{})
	rows := openChanges(t, h, gh)
	if len(rows) != 1 {
		t.Fatalf("changes = %+v", rows)
	}
	if _, err := db.DeclineToolChangeWithEvent(ctx, h.pool, "acct-1", rows[0].ID, "acct-1"); err != nil {
		t.Fatal(err)
	}
	h.core.setPolicy(PinPolicyAutoAccept)
	h.clock.Advance(6 * time.Minute) // past the credential cache
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 0 {
		t.Fatalf("a declined hash was served: %v", got)
	}
	if e := h.grantRow(gh).Tools["echo"]; e.Hash != hashA {
		t.Fatalf("grant now pins %s, want the original %s", e.Hash, hashA)
	}
	for _, e := range events(t, h, gh) {
		if e.Action == db.PinRepinned {
			t.Fatalf("auto_accept re-pinned a declined hash: %+v", e)
		}
	}
}

// A failed read of the declined-hash check must not let auto_accept re-pin: it counts as blocked,
// so the strict path hides the tool and records the change for the person.
func TestBlockedCheckReadErrorFailsClosed(t *testing.T) {
	orig := toolChangeHashBlocked
	toolChangeHashBlocked = func(context.Context, db.DBTX, string, string, string, string) (bool, error) {
		return false, errors.New("boom")
	}
	t.Cleanup(func() { toolChangeHashBlocked = orig })
	h := newHarness(t, nil)
	h.core.setPolicy(PinPolicyAutoAccept)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	hashA := h.up.hash("echo")
	h.up.setDescription("echo", "v2")
	h.clock.Advance(time.Minute)
	if got := toolNames(h.call(gh, "tools/list", map[string]any{})); len(got) != 0 {
		t.Fatalf("a read error let a drifted tool through: %v", got)
	}
	if e := h.grantRow(gh).Tools["echo"]; e.Hash != hashA {
		t.Fatalf("grant now pins %s, want the original %s", e.Hash, hashA)
	}
	for _, e := range events(t, h, gh) {
		if e.Action == db.PinRepinned {
			t.Fatalf("auto_accept re-pinned on a read error: %+v", e)
		}
	}
	if rows := openChanges(t, h, gh); len(rows) != 1 || rows[0].Kind != db.ToolChangeChanged {
		t.Fatalf("changes = %+v, want one changed row", rows)
	}
}

// Definitions are stored only for changes the caps admitted: a capped change leaves no row behind.
func TestCappedDriftStoresNoDefinition(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.ChangeLimits = db.ToolChangeLimits{Session: 1, Account: 10} })
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{"echo": "allow"}), Preset: db.PresetCustom, KnownTools: h.known()})
	h.up.addTool(fakeTool{Name: "x1", Description: "n1", Schema: `{"type":"object"}`})
	h.up.addTool(fakeTool{Name: "x2", Description: "n2", Schema: `{"type":"object"}`})
	h.clock.Advance(time.Minute)
	h.call(gh, "tools/list", map[string]any{})
	rows := openChanges(t, h, gh)
	if len(rows) != 1 {
		t.Fatalf("recorded %d, want the cap of 1", len(rows))
	}
	stored := 0
	for _, name := range []string{"x1", "x2"} {
		_, err := db.GetToolDefinition(context.Background(), h.pool, h.up.hash(name))
		switch {
		case err == nil:
			stored++
			if h.up.hash(name) != rows[0].NewHash {
				t.Fatalf("stored the definition of %s, but the recorded change is %s", name, rows[0].Tool)
			}
		case !errors.Is(err, db.ErrToolDefinitionNotFound):
			t.Fatalf("%s: %v", name, err)
		}
	}
	if stored != 1 {
		t.Fatalf("%d definitions stored, want exactly the recorded one", stored)
	}
}

// A listing cut at maxTools has not seen every tool: a grant on a tool past the cut is not removed.
func TestRemovedIsNotRecordedFromAnIncompleteListing(t *testing.T) {
	tools := make([]fakeTool, 0, maxTools+1)
	for i := 0; i <= maxTools; i++ {
		tools = append(tools, fakeTool{Name: fmt.Sprintf("t%d", i), Description: "d", Schema: `{"type":"object"}`})
	}
	h := newHarness(t, nil, tools...)
	gh := h.grantSpec("alpha", GrantSpec{Tools: h.pins(map[string]string{fmt.Sprintf("t%d", maxTools): "allow"}), Preset: db.PresetCustom})
	h.call(gh, "tools/list", map[string]any{})
	for _, r := range openChanges(t, h, gh) {
		if r.Kind == db.ToolChangeRemoved {
			t.Fatalf("a removed row was recorded from an incomplete listing: %+v", r)
		}
	}
}
