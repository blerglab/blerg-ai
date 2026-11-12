package shellkeeper

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

// A write that times out part way leaves half a frame on the stream. The channel
// must close rather than let a later frame follow the fragment.
func TestRound11PartialWriteClosesChannel(t *testing.T) {
	server, client := localPair(t)
	if err := server.SetWriteBuffer(4096); err != nil {
		t.Fatal(err)
	}
	ch := newChannel(server, bytes.Repeat([]byte{3}, 32), "keeper", "client", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := ch.SendContext(ctx, strings.Repeat("x", 200*1024)); err == nil {
		t.Fatal("a frame larger than the unread buffer was reported sent")
	}
	if err := ch.Send("next"); err == nil {
		t.Fatal("a frame was sent after a partial write")
	}
	if err := client.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, client); err != nil {
		t.Fatal("the peer did not see the stream end after the fragment", err)
	}
}
