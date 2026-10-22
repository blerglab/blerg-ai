package server

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/ticket"
	"net/http"
	"strings"
	"testing"
)

func TestUnpinLegacyBoardKeys(t *testing.T) {
	for _, board := range []string{"", "board-a"} {
		for _, stored := range []string{"#45", "Blerg #45"} {
			for _, asked := range []string{"#45", stored} {
				for _, source := range []string{"", ticket.SourceBoard} {
					t.Run(board+"/"+stored+"/"+asked+"/"+source, func(t *testing.T) {
						pin := ticket.Summary{Source: ticket.SourceBoard, Key: stored, BoardID: board, Title: "Fixture"}
						got, reason := unpinTarget([]ticket.Summary{pin}, source, asked)
						if got == nil || got.Key != stored || got.BoardID != board {
							t.Fatalf("target=%+v reason=%s", got, reason)
						}
					})
				}
			}
		}
	}
	for _, asked := range []string{"Blerg #45", "board-a#45"} {
		pin := ticket.Summary{Source: ticket.SourceBoard, Key: "Blerg #45", BoardID: "board-a"}
		got, reason := unpinTarget([]ticket.Summary{pin}, ticket.SourceBoard, asked)
		if got == nil {
			t.Fatalf("%s: %s", asked, reason)
		}
	}
}

func TestUnpinLegacyBoardAmbiguityAndScope(t *testing.T) {
	pins := []ticket.Summary{
		{Source: ticket.SourceBoard, Key: "First #45", BoardID: "board-a"},
		{Source: ticket.SourceBoard, Key: "Second #45", BoardID: "board-b"},
	}
	for _, source := range []string{"", ticket.SourceBoard} {
		if got, _ := unpinTarget(pins, source, "#45"); got != nil {
			t.Fatal("unqualified ambiguous card matched")
		}
	}
	for _, asked := range []string{"First #45", "board-a#45"} {
		got, reason := unpinTarget(pins, ticket.SourceBoard, asked)
		if got == nil || got.BoardID != "board-a" {
			t.Fatalf("%s: %+v %s", asked, got, reason)
		}
	}
	if got, _ := unpinTarget(pins, ticket.SourceBoard, "board-c#45"); got != nil {
		t.Fatal("matched another board")
	}
	if got, _ := unpinTarget(pins, "jira", "#45"); got != nil {
		t.Fatal("matched another source")
	}
}

func TestPostTicketsUnpinsLegacyBoardEvent(t *testing.T) {
	for _, board := range []string{"", "board-a"} {
		for _, key := range []string{"Blerg #45", "#45"} {
			for _, asked := range []string{key, "#45"} {
				t.Run(board+"/"+key+"/"+asked, func(t *testing.T) {
					fx := newWorkFx(t)
					payload, err := json.Marshal(ticket.Payload{Action: "pin", Ticket: ticket.Ticket{Source: ticket.SourceBoard, Key: key, BoardID: board, Title: "Fixture ticket"}})
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, fx.sess, newUUID(), "ticket", string(payload)); err != nil {
						t.Fatal(err)
					}
					code, body := fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "unpin", "source": ticket.SourceBoard, "key": asked})
					if code != http.StatusCreated {
						t.Fatalf("status=%d body=%v", code, body)
					}
					pinned, ok := body["pinned"].([]any)
					if !ok || len(pinned) != 0 {
						t.Fatalf("pinned=%v", body["pinned"])
					}
					stored, _ := body["ticket"].(map[string]any)
					if stored["key"] != key {
						t.Fatal("unpin event changed the stored key")
					}
					live := fx.getLive(fx.sess, fx.token(topicOwner, "human"), "")
					if !strings.Contains(live.Body.String(), `"tickets":[]`) {
						t.Fatalf("legacy event remained pinned after replay: %s", live.Body.String())
					}
				})
			}
		}
	}
}

// A legacy pin with no board ID matches a named board only by the board name stored in its key;
// with no board recorded at all, a named board is refused as ambiguous, never guessed.
func TestUnpinLegacyPinWithoutBoardIDNeedsItsBoardName(t *testing.T) {
	named := ticket.Summary{Source: ticket.SourceBoard, Key: "Blerg #45"}
	for _, asked := range []string{"Blerg #45", "#45"} {
		if got, reason := unpinTarget([]ticket.Summary{named}, ticket.SourceBoard, asked); got == nil {
			t.Fatalf("%s: %s", asked, reason)
		}
	}
	for _, asked := range []string{"Other #45", "board-x#45", "Blerg #46"} {
		if got, _ := unpinTarget([]ticket.Summary{named}, ticket.SourceBoard, asked); got != nil {
			t.Fatalf("%s unpinned a pin on another board", asked)
		}
	}

	bare := ticket.Summary{Source: ticket.SourceBoard, Key: "#45"}
	if got, reason := unpinTarget([]ticket.Summary{bare}, ticket.SourceBoard, "#45"); got == nil {
		t.Fatalf("#45: %s", reason)
	}
	for _, source := range []string{"", ticket.SourceBoard} {
		for _, asked := range []string{"Other #45", "board-x#45"} {
			got, reason := unpinTarget([]ticket.Summary{bare}, source, asked)
			if got != nil {
				t.Fatalf("%s matched a pin with no recorded board", asked)
			}
			if !strings.Contains(reason, "no recorded board") || !strings.Contains(reason, "#45") {
				t.Fatalf("%s: reason %q", asked, reason)
			}
		}
	}
}

// Review N1: #45 pinned on board-a and a legacy #45 with no board recorded. The qualified key
// unpins the board-a pin (the board-less one is not refused while another pin matched), and the
// bare key then unpins the legacy one; before that, the bare key is ambiguous.
func TestUnpinBoardPinBesideABoardlessLegacyPin(t *testing.T) {
	onA := ticket.Summary{Source: ticket.SourceBoard, Key: "#45", BoardID: "board-a", Title: "On A"}
	bare := ticket.Summary{Source: ticket.SourceBoard, Key: "#45", Title: "Legacy"}
	pins := []ticket.Summary{bare, onA}
	if got, _ := unpinTarget(pins, ticket.SourceBoard, "#45"); got != nil {
		t.Fatal("bare #45 guessed between two pins")
	}
	for _, source := range []string{"", ticket.SourceBoard} {
		got, reason := unpinTarget(pins, source, "board-a#45")
		if got == nil || got.BoardID != "board-a" {
			t.Fatalf("source %q: board-a#45 = %+v (%s)", source, got, reason)
		}
	}
	got, reason := unpinTarget([]ticket.Summary{bare}, ticket.SourceBoard, "#45")
	if got == nil || got.Title != "Legacy" {
		t.Fatalf("after unpinning board-a's, #45 = %+v (%s)", got, reason)
	}
	// A named board with no pin on it still refuses rather than guess the board-less pin.
	if got, reason := unpinTarget(pins, ticket.SourceBoard, "board-c#45"); got != nil || !strings.Contains(reason, "no recorded board") {
		t.Fatalf("board-c#45 = %+v (%s)", got, reason)
	}
}

// The same through the API: both pins are recorded events; the qualified key, then the bare key,
// leave nothing pinned.
func TestPostTicketsUnpinsBoardPinThenBoardlessLegacyPin(t *testing.T) {
	fx := newWorkFx(t)
	for _, pin := range []ticket.Ticket{
		{Source: ticket.SourceBoard, Key: "#45", Title: "Legacy"},
		{Source: ticket.SourceBoard, Key: "#45", BoardID: "board-a", Title: "On A"},
	} {
		payload, err := json.Marshal(ticket.Payload{Action: "pin", Ticket: pin})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.AppendAgentEvent(t.Context(), fx.pool, fx.sess, newUUID(), "ticket", string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	for i, asked := range []string{"board-a#45", "#45"} {
		code, body := fx.postWork("tickets", fx.sess, fx.sessTok, map[string]any{"action": "unpin", "source": ticket.SourceBoard, "key": asked})
		if code != http.StatusCreated {
			t.Fatalf("unpin %s: status=%d body=%v", asked, code, body)
		}
		pinned, _ := body["pinned"].([]any)
		if len(pinned) != 1-i {
			t.Fatalf("after unpin %s: pinned=%v", asked, body["pinned"])
		}
	}
}
