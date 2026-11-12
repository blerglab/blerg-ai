//go:build linux

package shellkeeper

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestReadinessUsesManagerSocketAndRefusesUnavailableManager(t *testing.T) {
	if err := NotifyReady(t.Context(), ""); err != nil {
		t.Fatal("standalone mode", err)
	}
	path := filepath.Join(t.TempDir(), "notify.sock")
	if err := NotifyReady(t.Context(), path); err == nil {
		t.Fatal("unavailable notification socket accepted")
	}
	listener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err = NotifyReady(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	if err = listener.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 128)
	n, _, err := listener.ReadFromUnix(buffer)
	if err != nil || string(buffer[:n]) != "READY=1" {
		t.Fatal("readiness notification", err)
	}
}
