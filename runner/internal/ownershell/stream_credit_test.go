package ownershell

import "testing"

func TestStreamCreditDuplicateAckCannotRefillOutstandingBytes(t *testing.T) {
	credit := NewStreamCredit(16, 0)
	if !credit.Reserve(0, 8, false) || credit.Committed(0, 8) != nil {
		t.Fatal("first frame refused")
	}
	if !credit.Reserve(8, 8, false) || credit.Committed(8, 8) != nil {
		t.Fatal("second frame refused")
	}
	if credit.Available() != 0 {
		t.Fatal("window overcommitted")
	}
	if err := credit.Acknowledge(8); err != nil {
		t.Fatal(err)
	}
	if credit.Available() != 8 {
		t.Fatal("ack did not release exactly received bytes")
	}
	for range 10 {
		if err := credit.Acknowledge(8); err != nil {
			t.Fatal("duplicate must be idempotent", err)
		}
	}
	if credit.Available() != 8 {
		t.Fatal("duplicate ack released outstanding bytes")
	}
	if credit.Acknowledge(17) == nil || credit.Acknowledge(7) == nil {
		t.Fatal("future/stale ack accepted")
	}
}

func TestStreamCreditEvictionGapDoesNotMintCapacity(t *testing.T) {
	credit := NewStreamCredit(16, 0)
	if !credit.Reserve(0, 8, false) || credit.Committed(0, 8) != nil {
		t.Fatal("frame refused")
	}
	if credit.Reserve(1000, 8, true) {
		t.Fatal("gap overlapped old unacknowledged frames")
	}
	if err := credit.Acknowledge(8); err != nil {
		t.Fatal(err)
	}
	if !credit.Reserve(1000, 8, true) || credit.Committed(1000, 8) != nil {
		t.Fatal("gap handoff refused")
	}
	if !credit.Reserve(1008, 8, false) || credit.Committed(1008, 8) != nil {
		t.Fatal("post-gap frame refused")
	}
	if credit.Available() != 0 {
		t.Fatal("post-gap window exceeded")
	}
	if credit.Acknowledge(500) == nil {
		t.Fatal("unsent gap bytes acknowledged")
	}
	if err := credit.Acknowledge(1008); err != nil {
		t.Fatal(err)
	}
	if credit.Available() != 8 {
		t.Fatal("evicted bytes refilled the full window")
	}
	if err := credit.Acknowledge(1008); err != nil || credit.Available() != 8 {
		t.Fatal("duplicate gap ack refilled outstanding output")
	}
}

func TestStreamCreditAcknowledgmentRequiresTransportCommit(t *testing.T) {
	credit := NewStreamCredit(16, 100)
	if !credit.Reserve(100, 8, false) {
		t.Fatal("reservation refused")
	}
	if credit.Acknowledge(108) == nil {
		t.Fatal("queued frame acknowledged before commit")
	}
	if credit.Committed(101, 8) == nil {
		t.Fatal("out-of-order committed frame accepted")
	}
	if err := credit.Committed(100, 8); err != nil {
		t.Fatal(err)
	}
	if err := credit.Acknowledge(108); err != nil {
		t.Fatal(err)
	}
	if credit.Available() != 16 {
		t.Fatal("committed frame not fully credited")
	}
}
