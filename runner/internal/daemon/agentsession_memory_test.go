package daemon

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

type memoryDiscardSender struct{}

func (memoryDiscardSender) Send(any) error { return nil }

func retainedHeap() uint64 {
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// A persistence acknowledgment releases ownership of the raw payload, even
// when acknowledgments arrive out of order and the buffer keeps its capacity.
func TestEmitterAckReleasesPayloads(t *testing.T) {
	emitter := newWSEmitter(memoryDiscardSender{}, "memory-fixture", nil)
	before := retainedHeap()
	const count = 300
	for i := 0; i < count; i++ {
		emitter.Emit(agent.Event{ClientEventID: fmt.Sprint(i), Kind: "assistant_text", Payload: map[string]any{"text": strings.Repeat("x", 64*1024), "done": true}})
	}
	queued := retainedHeap()
	// A middle acknowledgment must preserve the order and payloads of survivors.
	emitter.ack("150")
	if emitter.pending[149].clientEventID != "149" || emitter.pending[150].clientEventID != "151" {
		t.Fatal("acknowledgment reordered surviving events")
	}
	for i := count - 1; i >= 0; i-- {
		emitter.ack(fmt.Sprint(i))
	}
	if emitter.pendingCount() != 0 {
		t.Fatal("acknowledged events remain pending")
	}
	after := retainedHeap()
	var retainedBytes int
	for _, entry := range emitter.pending[:cap(emitter.pending)] {
		retainedBytes += len(entry.raw)
	}
	t.Logf("300 x 64 KiB events: heap before=%d queued=%d acknowledged=%d backing_payload_bytes=%d", before, queued, after, retainedBytes)
	if retainedBytes != 0 {
		t.Errorf("acknowledged backing storage retains %d payload bytes", retainedBytes)
	}
	runtime.KeepAlive(emitter)
}
