package ownershell

import (
	"bytes"
	"testing"
)

func TestHistoryEvictionReportsAnExplicitGap(t *testing.T) {
	h := NewHistory(32)
	if err := h.Append(bytes.Repeat([]byte("a"), 48)); err != nil {
		t.Fatal(err)
	}
	frame, err := h.Read(0, 16)
	if err != nil || !frame.Gap || frame.Offset != 16 || frame.End != 48 || len(frame.Data) != 16 {
		t.Fatal("eviction silently lost stream offsets", frame, err)
	}
	if h.Acknowledge(49) == nil {
		t.Fatal("acknowledged output that never existed")
	}
	if h.Acknowledge(32) == nil {
		t.Fatal("acknowledged unsent output")
	}
	if err = h.Sent(32); err != nil {
		t.Fatal(err)
	}
	if err = h.Acknowledge(32); err != nil {
		t.Fatal(err)
	}
	if h.Acknowledge(31) == nil {
		t.Fatal("ack moved backward")
	}
	if h.Acknowledged() != 32 {
		t.Fatal("lost acknowledgment")
	}
	next, err := h.Read(32, 16)
	if err != nil || next.Gap || next.Offset != 32 || next.End != 48 || len(next.Data) != 16 {
		t.Fatal("handoff skipped output", next, err)
	}
}

func TestHistoryRetainsUntilAckAndDoesNotSharePayloads(t *testing.T) {
	h := NewHistory(32)
	input := []byte("private-output")
	if err := h.Append(input); err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'
	first, err := h.Read(0, MaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	first.Data[0] = 'X'
	again, err := h.Read(0, MaxFrame)
	if err != nil || string(again.Data) != "private-output" {
		t.Fatal("payload mutated or discarded before ack")
	}
	if err = h.Sent(again.End); err != nil {
		t.Fatal(err)
	}
	if err = h.Acknowledge(again.End); err != nil {
		t.Fatal(err)
	}
	replay, err := h.Read(0, MaxFrame)
	if err != nil || string(replay.Data) != "private-output" {
		t.Fatal("ack erased bounded replay")
	}
	if _, err = h.Read(again.End+1, MaxFrame); err == nil {
		t.Fatal("future replay offset accepted")
	}
}

func TestHistoryAllocationStaysWithinItsBound(t *testing.T) {
	h := NewHistory(100)
	for range 100 {
		if err := h.Append(bytes.Repeat([]byte("x"), 37)); err != nil {
			t.Fatal(err)
		}
		if cap(h.data) > 100 {
			t.Fatal("ring allocation exceeded its byte bound", cap(h.data))
		}
	}
}

func TestHistoryReceivesHelperOffsetsWithoutRenumbering(t *testing.T) {
	h := NewHistory(32)
	gap, err := h.Receive(HistoryFrame{Offset: 1000, End: 1006, Gap: true, Data: []byte("abcdef")})
	if err != nil || !gap {
		t.Fatal("explicit source gap lost", err)
	}
	gap, err = h.Receive(HistoryFrame{Offset: 1000, End: 1006, Gap: true, Data: []byte("abcdef")})
	if err != nil || gap {
		t.Fatal("duplicate source gap repeated", err)
	}
	if _, err = h.Receive(HistoryFrame{Offset: 1003, End: 1009, Data: []byte("defghi")}); err != nil {
		t.Fatal(err)
	}
	frame, err := h.Read(0, MaxFrame)
	if err != nil || frame.Offset != 1000 || frame.End != 1009 || !frame.Gap || string(frame.Data) != "abcdefghi" {
		t.Fatal("source offsets lost or duplicated", frame, err)
	}
	if _, err = h.Receive(HistoryFrame{Offset: 1004, End: 1010, Data: []byte("wrong!")}); err == nil {
		t.Fatal("conflicting overlap accepted")
	}
	if _, err = h.Receive(HistoryFrame{Offset: 1010, End: 1011, Data: []byte("x")}); !Unknown(err) {
		t.Fatal("silent source hole accepted", err)
	}
	unchanged, _ := h.Read(1000, MaxFrame)
	if string(unchanged.Data) != "abcdefghi" || unchanged.End != 1009 {
		t.Fatal("invalid frame mutated retained data")
	}
}
