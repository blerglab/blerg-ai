package server

import (
	"encoding/json"
	"testing"
)

func TestRecoveryReadyKillsMissingDelegatedRecordsButKeepsLegacyDiscovery(t *testing.T) {
	fx := newDelegFx(t)
	missing, legacy := newUUID(), newUUID()
	if err := queueDelegationRecoveryReady(t.Context(), fx.hub, fx.pool, fx.dc, []string{missing, legacy}, []string{missing}); err != nil {
		t.Fatal(err)
	}
	killed := false
	for {
		select {
		case raw := <-fx.dc.send:
			var msg struct {
				Type      string `json:"type"`
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type == "kill_session" {
				if msg.SessionID != missing {
					t.Fatalf("killed legacy discovery %q", msg.SessionID)
				}
				killed = true
			}
			if msg.Type == "daemon_recovery_ready" {
				if !killed {
					t.Fatal("ready preceded missing delegated record's kill")
				}
				return
			}
		default:
			t.Fatal("ready was not queued")
		}
	}
}

func TestChildStartRefusesOldRecoveryPeer(t *testing.T) {
	fx := newDelegFx(t)
	fx.dc.RecoveryReadyCapable = false
	err := fx.api.validateChildStart(t.Context(), childStart{Engine: "codex", Runtime: "docker", Repo: "app"}, fx.dc)
	if err == nil || err.Status != 409 {
		t.Fatalf("old peer was not refused: %+v", err)
	}
}
