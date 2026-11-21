//go:build linux

package shellkeeper

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// Channel permits framed service operations only after both identity proofs.
// Every frame is direction-bound, authenticated and strictly ordered. A relay
// that forwarded the handshake cannot add or replay commands on this stream.
type Channel struct {
	conn               *net.UnixConn
	scan               *bufio.Scanner
	key                []byte
	incoming, outgoing string
	sent, received     uint64
	write              chan struct{}
	valid              func() bool
	generation         string
}
type streamFrame struct {
	Sequence uint64          `json:"sequence"`
	Body     json.RawMessage `json:"body"`
	MAC      []byte          `json:"mac"`
}

func newChannel(conn *net.UnixConn, key []byte, incoming, outgoing string, valid func() bool) *Channel {
	scan := bufio.NewScanner(conn)
	scan.Buffer(make([]byte, 4096), wireLimit)
	return &Channel{conn: conn, scan: scan, key: key, incoming: incoming, outgoing: outgoing, valid: valid, write: make(chan struct{}, 1)}
}

func authenticatedChannel(conn *net.UnixConn, key []byte, incoming, outgoing string, valid func() bool, scan *bufio.Scanner) (*Channel, error) {
	// The short pre-authentication deadline must not become an idle-session
	// deadline. Lifetime/access expiry continue in the per-shell lease monitor.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, refusal("channel_deadline_clear", err)
	}
	channel := newChannel(conn, key, incoming, outgoing, valid)
	if scan != nil {
		channel.scan = scan
	}
	return channel, nil
}
func streamMAC(key []byte, direction string, f streamFrame) []byte {
	claims := struct {
		Sequence uint64          `json:"sequence"`
		Body     json.RawMessage `json:"body"`
	}{f.Sequence, f.Body}
	data, _ := json.Marshal(claims)
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte("blerg-shell-keeper-v2/service/" + direction + "\x00"))
	_, _ = m.Write(data)
	return m.Sum(nil)
}
func (c *Channel) Generation() string { return c.generation }
func (c *Channel) Current() bool      { return c.authorized() }
func (c *Channel) Close() error       { return c.conn.Close() }
func (c *Channel) authorized() bool   { return c.valid == nil || c.valid() }
func (c *Channel) Send(value any) error {
	return c.SendContext(context.Background(), value)
}
func (c *Channel) SendContext(ctx context.Context, value any) error {
	select {
	case c.write <- struct{}{}:
	case <-ctx.Done():
		return refusal("channel_send_cancelled", ctx.Err())
	}
	defer func() { <-c.write }()
	if ctx.Err() != nil {
		return refusal("channel_send_cancelled", ctx.Err())
	}
	if !c.authorized() || c.sent == ^uint64(0) {
		return refusal("channel_authority", ErrRefused)
	}
	body, err := json.Marshal(value)
	if err != nil {
		return refusal("channel_encode", err)
	}
	f := streamFrame{Sequence: c.sent + 1, Body: body}
	f.MAC = streamMAC(c.key, c.outgoing, f)
	data, err := json.Marshal(f)
	if err != nil || len(data)+1 >= wireLimit {
		return refusal("channel_bounds", ErrRefused)
	}
	deadline := time.Now().Add(2 * time.Second)
	if until, ok := ctx.Deadline(); ok && until.Before(deadline) {
		deadline = until
	}
	if err = c.conn.SetWriteDeadline(deadline); err != nil {
		return refusal("channel_deadline", err)
	}
	if _, err = c.conn.Write(append(data, '\n')); err != nil {
		// A write that stops part way leaves half a frame on the stream, so no
		// later frame can be trusted: close it and let the caller reconnect.
		_ = c.conn.Close()
		return refusal("channel_write", err)
	}
	c.sent = f.Sequence
	return nil
}
func (c *Channel) Receive(value any) error {
	if !c.scan.Scan() {
		if err := c.scan.Err(); err != nil {
			return refusal("channel_read", err)
		}
		return refusal("channel_eof", ErrRefused)
	}
	var f streamFrame
	if json.Unmarshal(c.scan.Bytes(), &f) != nil || !c.authorized() || c.received == ^uint64(0) || f.Sequence != c.received+1 || !hmac.Equal(f.MAC, streamMAC(c.key, c.incoming, f)) {
		return refusal("channel_proof", ErrRefused)
	}
	if err := json.Unmarshal(f.Body, value); err != nil {
		return refusal("channel_body", err)
	}
	c.received = f.Sequence
	return nil
}

// WithSession pins the generation obtained at the startup fence. It never
// follows a replacement keeper or silently accepts a new custody universe.
func WithSession(ctx context.Context, dir string, epoch Epoch, generation string, use func(*Channel, Snapshot) error) error {
	return withSession(ctx, dir, epoch, generation, use, waitRetry)
}

func withSession(ctx context.Context, dir string, epoch Epoch, generation string, use func(*Channel, Snapshot) error, pause func(context.Context, time.Duration) bool) error {
	if len(generation) != 64 {
		return refusal("channel_generation", ErrRefused)
	}
	until := time.Now().Add(2 * time.Second)
	for {
		started := false
		_, err := requestUsing(ctx, dir, epoch, "service", Version, generation, func(ch *Channel, view Snapshot) error { started = true; return use(ch, view) })
		if started || !errors.Is(err, errBusy) {
			return err
		}
		if !time.Now().Before(until) || !pause(ctx, 25*time.Millisecond) {
			return refusal("channel_retry_exhausted", ErrRefused)
		}
	}
}

func InspectSnapshot(ctx context.Context, dir string, epoch Epoch) (Snapshot, error) {
	return inspectRetry(ctx, dir, epoch)
}
