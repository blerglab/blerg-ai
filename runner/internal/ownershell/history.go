package ownershell

import (
	"bytes"
	"sync"
)

// HistoryFrame uses helper-assigned stream offsets at every transport stage.
// Gap is explicit eviction, not permission to skip an unreceived output range.
type HistoryFrame struct {
	Offset uint64
	End    uint64
	Gap    bool
	Data   []byte
}

// History retains bounded private volatile output independently of access or
// output credit. The helper may keep appending while all terminal access is off.
type History struct {
	mu             sync.Mutex
	capacity       int
	data           []byte
	end, sent, ack uint64
}

func NewHistory(capacity int) *History {
	if capacity <= 0 || capacity > ScrollbackBytes {
		capacity = ScrollbackBytes
	}
	return &History{capacity: capacity}
}

func (*History) String() string   { return "<shell output history>" }
func (*History) GoString() string { return "<shell output history>" }

func (h *History) Append(data []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.appendLocked(data)
}

func (h *History) appendLocked(data []byte) error {
	if uint64(len(data)) > ^uint64(0)-h.end {
		return ErrLimit
	}
	if len(data) > 0 && h.data == nil {
		h.data = make([]byte, 0, h.capacity)
	}
	h.end += uint64(len(data))
	if len(data) >= h.capacity {
		h.data = append(h.data[:0], data[len(data)-h.capacity:]...)
		return nil
	}
	if remove := len(h.data) + len(data) - h.capacity; remove > 0 {
		copy(h.data, h.data[remove:])
		h.data = h.data[:len(h.data)-remove]
	}
	h.data = append(h.data, data...)
	return nil
}

func (h *History) Read(offset uint64, limit int) (HistoryFrame, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if offset > h.end || limit <= 0 {
		return HistoryFrame{}, ErrDenied
	}
	start := h.end - uint64(len(h.data))
	gap := offset < start
	if gap {
		offset = start
	}
	at := int(offset - start) //nolint:gosec // offset is in [end-len(data), end], so this is bounded by len(data) <= 256 KiB.
	count := min(limit, MaxFrame, len(h.data)-at)
	return HistoryFrame{Offset: offset, End: h.end, Gap: gap, Data: append([]byte(nil), h.data[at:at+count]...)}, nil
}

// Sent records actual committed forwarding, not just a queued or read frame.
// Retransmission of previously sent offsets never moves the high-water mark back.
func (h *History) Sent(end uint64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if end > h.end {
		return ErrDenied
	}
	h.sent = max(h.sent, end)
	return nil
}

func (h *History) Acknowledge(offset uint64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if offset < h.ack || offset > h.sent {
		return ErrDenied
	}
	h.ack = offset
	return nil
}
func (h *History) Acknowledged() uint64 { h.mu.Lock(); defer h.mu.Unlock(); return h.ack }

// Receive preserves the helper's global byte positions through intermediate
// queues. It removes verified replay overlap and refuses an unexplained hole.
// The returned gap is newly missing local data, not a repeated source warning.
func (h *History) Receive(frame HistoryFrame) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(frame.Data) == 0 || len(frame.Data) > MaxFrame || uint64(len(frame.Data)) > ^uint64(0)-frame.Offset {
		return false, ErrDenied
	}
	end := frame.Offset + uint64(len(frame.Data))
	if frame.End < end {
		return false, ErrDenied
	}
	if frame.Offset > h.end && !frame.Gap {
		return false, ErrUnknown
	}
	base := h.end - uint64(len(h.data))
	overlapStart := max(frame.Offset, base)
	overlapEnd := min(end, h.end)
	if overlapStart < overlapEnd {
		a := int(overlapStart - frame.Offset) //nolint:gosec // the overlap is inside frame.Data, bounded by MaxFrame.
		b := int(overlapStart - base)         //nolint:gosec // the overlap is inside h.data, bounded by ScrollbackBytes.
		n := int(overlapEnd - overlapStart)   //nolint:gosec // the overlap is bounded by the incoming frame size.
		if !bytes.Equal(frame.Data[a:a+n], h.data[b:b+n]) {
			return false, ErrDenied
		}
	}
	gap := frame.Offset > h.end
	if gap {
		h.data = h.data[:0]
		h.end = frame.Offset
	}
	skip := uint64(0)
	if frame.Offset < h.end {
		skip = min(uint64(len(frame.Data)), h.end-frame.Offset)
	}
	return gap, h.appendLocked(frame.Data[skip:])
}

// Bounds exposes positions only, never the private retained payload.
func (h *History) Bounds() (uint64, uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.end - uint64(len(h.data)), h.end
}

// Clear discards terminal bytes when custody becomes terminal.
func (h *History) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	clear(h.data)
	h.data = nil
}
