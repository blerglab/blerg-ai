package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// File replies have the writer's lowest lane. Every other frame goes first, and
// a reply larger than a chunk goes as chunks so it never holds the socket long.
func (c *WSClient) SendWorkspaceFileResult(result protocol.WorkspaceFileResult) error {
	return c.SendWorkspaceFileResultBy(result, time.Time{})
}

// SendWorkspaceFileResultBy is SendWorkspaceFileResult for a reply the server stops
// waiting for at notAfter: a reply that could only start after that is not sent
// (a large frame the server would read and throw away).
func (c *WSClient) SendWorkspaceFileResultBy(result protocol.WorkspaceFileResult, notAfter time.Time) error {
	if notAfter.IsZero() {
		notAfter = time.Now().Add(workspaceReplyTTL)
	}
	return c.SendWorkspaceFileResultContext(context.Background(), result, notAfter)
}
func (c *WSClient) sendWorkspaceFileResult(result protocol.WorkspaceFileResult, timeout time.Duration) error {
	return c.sendBoundedReply(result, timeout, workspaceWriterWait, time.Time{})
}

// workspaceWriterWait is how long a file reply waits for a busy writer before it gives up.
const workspaceWriterWait = 2 * time.Second

// workspaceChunkWrite bounds the write of one chunk, the stall protection of a transfer.
const workspaceChunkWrite = time.Second

// workspaceReplyTTL is how long after the daemon receives a read request its reply is still
// worth sending: the server stops waiting 10 s after it sent the request.
const workspaceReplyTTL = 9 * time.Second

func (c *WSClient) sendBoundedWorkspaceReply(result any, timeout time.Duration) error {
	return c.sendBoundedReply(result, timeout, 0, time.Time{})
}

// WorkspaceFileChunks reports whether this link's server offered file chunks.
func (c *WSClient) WorkspaceFileChunks() bool { return c.fileChunks.Load() }

func (c *WSClient) currentConn() *websocket.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

type fairWriteKey struct{}
type fairWriteOptions struct {
	connection        *websocket.Conn
	startBy, notAfter time.Time
	timeout           time.Duration
	chunked           bool
}

// Queue time and actual write time have separate bounds. The reply stays bound
// to the original socket, and its TTL applies both before and during delivery.
func (c *WSClient) sendBoundedReply(result any, timeout, wait time.Duration, notAfter time.Time) error {
	return c.sendBoundedReplyTo(result, timeout, wait, notAfter, c.currentConn())
}
func (c *WSClient) sendBoundedReplyTo(result any, timeout, wait time.Duration, notAfter time.Time, conn *websocket.Conn) error {
	return c.sendBoundedReplyContext(context.Background(), conn, result, timeout, wait, notAfter)
}
func (c *WSClient) sendBoundedReplyContext(parent context.Context, conn *websocket.Conn, result any, timeout, wait time.Duration, notAfter time.Time) error {
	if file, ok := result.(protocol.WorkspaceFileResult); ok {
		return c.sendFileReply(parent, conn, file, timeout, wait, notAfter)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return c.sendBoundedFrame(parent, conn, raw, timeout, wait, notAfter)
}

// sendBoundedFrame binds the frame to conn and bounds its wait and its write.
func (c *WSClient) sendBoundedFrame(parent context.Context, conn *websocket.Conn, raw []byte, timeout, wait time.Duration, notAfter time.Time) error {
	ctx, options, cancel, err := boundedWrite(parent, conn, timeout, wait, notAfter)
	if err != nil {
		return err
	}
	defer cancel()
	return c.sendFair(context.WithValue(ctx, fairWriteKey{}, options), raw)
}

func boundedWrite(parent context.Context, conn *websocket.Conn, timeout, wait time.Duration, notAfter time.Time) (context.Context, fairWriteOptions, context.CancelFunc, error) {
	if conn == nil {
		return nil, fairWriteOptions{}, nil, errors.New("workspace file transport is disconnected")
	}
	if !notAfter.IsZero() && !time.Now().Before(notAfter) {
		return nil, fairWriteOptions{}, nil, errors.New("workspace file reply expired")
	}
	if wait <= 0 {
		wait = workspaceWriterWait
	}
	timeout = min(timeout, time.Second)
	options := fairWriteOptions{connection: conn, startBy: time.Now().Add(wait), notAfter: notAfter, timeout: timeout}
	ctx, cancel := context.WithTimeout(parent, wait+timeout)
	if notAfter.IsZero() {
		return ctx, options, cancel, nil
	}
	ctx, stop := context.WithDeadline(ctx, notAfter)
	return ctx, options, func() { stop(); cancel() }, nil
}

// sendFileReply queues the reply on the file lane. Chunks need a server that
// reassembles them; to any other, and for a reply that fits, it is one frame.
func (c *WSClient) sendFileReply(parent context.Context, conn *websocket.Conn, result protocol.WorkspaceFileResult, timeout, wait time.Duration, notAfter time.Time) error {
	ctx, options, cancel, err := boundedWrite(parent, conn, timeout, wait, notAfter)
	if err != nil {
		return err
	}
	defer cancel()
	writer, err := c.writerFor(conn)
	if err != nil {
		return err
	}
	if c.fileChunks.Load() && len(result.ContentB64) > protocol.WorkspaceFileChunkBytes {
		// A whole budget for each chunk, however little of the reply's time is
		// left: one cut short by that clock would close the link for nothing.
		options.chunked, options.timeout = true, workspaceChunkWrite
		frames, frame := fileChunkFrames(result)
		return writer.SendFile(context.WithValue(ctx, fairWriteKey{}, options), frames, frame, options.startBy)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return writer.SendFile(context.WithValue(ctx, fairWriteKey{}, options), 1, func(int) []byte { return raw }, options.startBy)
}

// fileChunkFrames splits a reply into its chunks and the closing result frame.
// Frames are built as they are written, so the reply is not held twice.
func fileChunkFrames(result protocol.WorkspaceFileResult) (int, func(int) []byte) {
	content := result.ContentB64
	chunks := (len(content) + protocol.WorkspaceFileChunkBytes - 1) / protocol.WorkspaceFileChunkBytes
	result.ContentB64, result.Chunks = "", chunks
	return chunks + 1, func(n int) []byte {
		if n == chunks {
			raw, _ := json.Marshal(result) //nolint:errchkjson // strings and integers only: this cannot fail
			return raw
		}
		end := min((n+1)*protocol.WorkspaceFileChunkBytes, len(content))
		raw, _ := json.Marshal(protocol.WorkspaceFileChunk{ //nolint:errchkjson // strings and integers only: this cannot fail
			WorkspaceRuntimeRef: result.WorkspaceRuntimeRef, Type: "workspace_file_chunk", RequestID: result.RequestID, SessionID: result.SessionID, Seq: n,
			Data: content[n*protocol.WorkspaceFileChunkBytes : end],
		})
		return raw
	}
}
