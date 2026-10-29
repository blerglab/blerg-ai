package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

func TestDesktopEnrollmentStoresAcknowledgesAndReconnects(t *testing.T) {
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	logs := captureLinkLog(t)
	token := "bdt_" + strings.Repeat("a", 64)
	id := "0d0d0d0d-0000-4000-8000-000000000001"
	var connections atomic.Int32
	hellos := make(chan protocol.DaemonHello, 4)
	acks := make(chan protocol.DesktopEnrollAck, 2)
	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var hello protocol.DaemonHello
		if conn.ReadJSON(&hello) != nil {
			return
		}
		hellos <- hello
		sendWelcome(conn, "workstation")
		if connections.Add(1) == 1 {
			message := protocol.DesktopEnroll{Type: "desktop_enroll", RequestID: "request", ConnectionGeneration: "generation", Token: token, Binding: desktopauth.Binding{DesktopID: id, OwnerID: "person", Revision: 2, ExpiresAt: time.Now().Add(time.Hour)}}
			if conn.WriteJSON(message) != nil {
				return
			}
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					return
				}
				var ack protocol.DesktopEnrollAck
				if json.Unmarshal(raw, &ack) == nil && ack.Type == "desktop_enroll_ack" {
					acks <- ack
					return
				}
			}
		}
		drain(conn)
	})
	c := NewWSClient(Config{ServerURL: ts.wsURL(), DaemonToken: fakeDaemonToken, DaemonMode: "local", DaemonName: "workstation", CredentialStateDir: state, ReconnectInitial: time.Millisecond, ReconnectMax: time.Millisecond})
	cancel := runClient(t, c)
	select {
	case h := <-hellos:
		if !h.DesktopEnrollment || h.Token != fakeDaemonToken {
			t.Fatal("enrollment not advertised")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
	}
	select {
	case ack := <-acks:
		if ack.RequestID != "request" || ack.ConnectionGeneration != "generation" {
			t.Fatal("incorrect ack")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ack")
	}
	select {
	case h := <-hellos:
		if h.DesktopID != id || h.Token != token {
			t.Fatal("reconnect did not load private credential")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no personal reconnect")
	}
	cancel()
	info, err := os.Stat(filepath.Join(state, "desktop-credential.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("credential permissions")
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("credential leaked to log")
	}
}
func TestDesktopCredentialStoreRejectsSharedAndLinkedDirectories(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0o700)
	c := DesktopCredential{DesktopID: "desktop", Token: "bdt_" + strings.Repeat("b", 64)}
	if err := StoreDesktopCredential(root, c); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(root, 0o755)
	if StoreDesktopCredential(root, c) == nil {
		t.Fatal("shared state accepted")
	}
	_ = os.Chmod(root, 0o700)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if StoreDesktopCredential(link, c) == nil {
		t.Fatal("linked state accepted")
	}
	c.Token = "shared"
	if StoreDesktopCredential(root, c) == nil {
		t.Fatal("shared bearer accepted")
	}
}

func TestDesktopEnrollmentRecoversRotationBeforeDelivery(t *testing.T) {
	state := t.TempDir()
	_ = os.Chmod(state, 0o700)
	id := "0d0d0d0d-0000-4000-8000-000000000001"
	first, second := "bdt_"+strings.Repeat("c", 64), "bdt_"+strings.Repeat("d", 64)
	var connections atomic.Int32
	hellos := make(chan protocol.DaemonHello, 8)
	ack := func(conn *websocket.Conn) bool {
		for {
			var m protocol.DesktopEnrollAck
			if conn.ReadJSON(&m) != nil {
				return false
			}
			if m.Type == "desktop_enroll_ack" {
				return true
			}
		}
	}
	issue := func(conn *websocket.Conn, token, request, generation string, revision int64) bool {
		return conn.WriteJSON(protocol.DesktopEnroll{Type: "desktop_enroll", Token: token, RequestID: request, ConnectionGeneration: generation, Binding: desktopauth.Binding{DesktopID: id, OwnerID: "person", Revision: revision, ExpiresAt: time.Now().Add(time.Hour)}}) == nil
	}
	ts := newTestServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var h protocol.DaemonHello
		if conn.ReadJSON(&h) != nil {
			return
		}
		hellos <- h
		switch connections.Add(1) {
		case 1:
			sendWelcome(conn, "workstation")
			if !issue(conn, first, "first", "generation-1", 2) {
				return
			}
			_ = ack(conn)
			// The browser rotated again, but this link dropped before that delivery.
		case 2:
			_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4401, "obsolete pending credential"))
		case 3:
			sendWelcome(conn, "workstation")
			if !issue(conn, second, "retry", "generation-2", 3) {
				return
			}
			_ = ack(conn)
		default:
			sendWelcome(conn, "workstation")
			drain(conn)
		}
	})
	c := NewWSClient(Config{ServerURL: ts.wsURL(), DaemonToken: fakeDaemonToken, DaemonName: "workstation", DaemonMode: "local", CredentialStateDir: state, ReconnectInitial: time.Millisecond, ReconnectMax: time.Millisecond})
	cancel := runClient(t, c)
	defer cancel()
	for i, want := range []string{fakeDaemonToken, first, fakeDaemonToken, second} {
		select {
		case h := <-hellos:
			if h.Token != want {
				t.Fatalf("connection %d did not recover enrollment", i+1)
			}
			if i == 2 && h.DesktopID != "" {
				t.Fatal("compatibility hello claimed a personal identity")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("enrollment recovery stalled")
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		credential, err := LoadDesktopCredential(state)
		if err == nil && credential != nil && credential.Token == second && !credential.EnrollmentPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("personal welcome did not confirm credential")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConfirmedDesktopCredentialNeverRecoversSharedAuthority(t *testing.T) {
	state := t.TempDir()
	_ = os.Chmod(state, 0o700)
	credential := DesktopCredential{DesktopID: "desktop", Token: "bdt_" + strings.Repeat("e", 64)}
	if err := StoreDesktopCredential(state, credential); err != nil {
		t.Fatal(err)
	}
	c := NewWSClient(Config{DaemonToken: fakeDaemonToken, CredentialStateDir: state})
	c.recoverPendingEnrollment()
	if c.enrollmentLegacy {
		t.Fatal("confirmed personal credential fell back to shared authority")
	}
	credential.EnrollmentPending = true
	if err := StoreDesktopCredential(state, credential); err != nil {
		t.Fatal(err)
	}
	c.recoverPendingEnrollment()
	if !c.enrollmentLegacy {
		t.Fatal("pending enrollment cannot recover")
	}
	if err := c.confirmDesktopEnrollment(credential.Token, credential.DesktopID); err != nil {
		t.Fatal(err)
	}
	c.recoverPendingEnrollment()
	if c.enrollmentLegacy {
		t.Fatal("confirmation did not permanently retire fallback")
	}
}
