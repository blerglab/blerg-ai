package shellruntime

import (
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestRetainedOutputQuarantinesPreparedEpoch(t *testing.T) {
	s := newRetainedOutput(1)
	s.suspend()
	s.prepared("rekey", 2)
	if err := s.receive(HelperFrame{Epoch: 2, Offset: 100, End: 103, Gap: true, Data: []byte("abc")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.read(2, 0); !errors.Is(err, ownershell.ErrDenied) {
		t.Fatal("uncommitted output escaped", err)
	}
	s.committed("wrong")
	if _, err := s.read(2, 0); !errors.Is(err, ownershell.ErrDenied) {
		t.Fatal("wrong transaction committed", err)
	}
	s.committed("rekey")
	f, err := s.read(2, 0)
	if err != nil || f.Offset != 100 || !f.Gap || string(f.Data) != "abc" {
		t.Fatalf("lost source positions: %+v %v", f, err)
	}
	if err := s.receive(HelperFrame{Epoch: 1, Offset: 103, End: 106, Data: []byte("old")}); !errors.Is(err, ownershell.ErrDenied) {
		t.Fatal("stale epoch admitted", err)
	}
}

func TestRetainedOutputAcknowledgesOnlyReceivedBytes(t *testing.T) {
	s := newRetainedOutput(1)
	if err := s.receive(HelperFrame{Epoch: 1, End: 100, Data: []byte("abc")}); err != nil {
		t.Fatal(err)
	}
	epoch, offset := s.acknowledgment()
	if epoch != 1 || offset != 3 {
		t.Fatalf("acknowledged unseen source bytes: %d %d", epoch, offset)
	}
	if err := s.receive(HelperFrame{Epoch: 1, Offset: 10, End: 13, Data: []byte("bad")}); !errors.Is(err, ownershell.ErrUnknown) {
		t.Fatal("unexplained gap accepted", err)
	}
	_, offset = s.acknowledgment()
	if offset != 3 {
		t.Fatal("failed receive moved ack", offset)
	}
	s.suspend()
	if _, err := s.read(1, 0); !errors.Is(err, ownershell.ErrDenied) {
		t.Fatal("suspended output escaped", err)
	}
}

func TestRetainedOutputConflictSuspendsAccessWithoutKilling(t *testing.T) {
	s := newRetainedOutput(1)
	if err := s.receive(HelperFrame{Epoch: 1, Offset: 0, End: 3, Data: []byte("abc")}); err != nil {
		t.Fatal(err)
	}
	if err := s.receive(HelperFrame{Epoch: 1, Offset: 1, End: 3, Data: []byte("XX")}); err == nil {
		t.Fatal("conflicting replay admitted")
	}
	if _, err := s.read(1, 0); !errors.Is(err, ownershell.ErrDenied) {
		t.Fatal("unknown stream still allows access", err)
	}
}
