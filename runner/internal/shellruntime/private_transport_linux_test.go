//go:build linux

package shellruntime

import (
	"testing"
	"time"
)

func TestPersistentTransportSupportsDeadlines(t *testing.T) {
	read, write, err := helperTransport(true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close(); _ = write.Close() }()
	if err = write.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal("private transport lost bounded writes", err)
	}
	if err = read.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal("private transport lost bounded reads", err)
	}
}
