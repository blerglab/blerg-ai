package server

import (
	"encoding/json"
	"testing"
	"time"
)

// Card #127 (3): a started session pins its own card; the parent's live state shows the child's
// current pin on its row, and a pin wakes the parent's panel like the child's other activity.
func TestLiveChildCarriesItsCurrentPin(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if got := telemetryChild(t, fx)["ticket"]; got != nil {
		t.Fatalf("a child with no pin reports one: %+v", got)
	}
	pin := func(action, key, title string) {
		seedEvent(t, fx.pool, child, time.Now(), "ticket", map[string]any{"action": action,
			"ticket": map[string]any{"source": "blerg-board", "key": key, "board_id": "board-1", "title": title, "status": "In progress"}})
	}
	pin("pin", "#122", "Build History fold")
	pin("pin", "#125", "Pins on Live")
	pin("unpin", "#125", "Pins on Live")
	got, _ := telemetryChild(t, fx)["ticket"].(map[string]any)
	want := map[string]any{"source": "blerg-board", "key": "#122", "board_id": "board-1", "title": "Build History fold", "status": "In progress"}
	raw, _ := json.Marshal(got)
	wantRaw, _ := json.Marshal(want)
	if string(raw) != string(wantRaw) {
		t.Fatalf("child pin = %s, want %s", raw, wantRaw)
	}
}

func TestChildPinNotifiesTheParent(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	var woke []string
	fx.hub.SetChildActivityHook(func(id string) { woke = append(woke, id) })
	fanOutServerEvent(fx.hub, child, newUUID(), "ticket", []byte(`{"action":"pin","ticket":{"source":"jira","key":"WEB-1","title":"x"}}`), 1, time.Now())
	if len(woke) != 1 || woke[0] != child {
		t.Fatalf("a child's pin did not wake its parent: %v", woke)
	}
}
