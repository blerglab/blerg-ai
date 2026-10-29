package ownershell

import (
	"bytes"
	"testing"
	"time"
)

func TestOutputPipeCoalescesInOrderWithinItsBound(t *testing.T) {
	p := NewOutputPipe(10)
	for _, part := range []string{"ab", "cde", "fgh"} {
		if !p.Write([]byte(part)) {
			t.Fatal("write within the bound refused")
		}
	}
	if p.Write([]byte("ijk")) {
		t.Fatal("write past the bound accepted")
	}
	if p.Len() != 8 {
		t.Fatal("refused write kept bytes")
	}
	first, ok := p.Read(5, nil)
	if !ok || string(first) != "abcde" {
		t.Fatalf("first read %q", first)
	}
	if !p.Write([]byte("ijk")) {
		t.Fatal("space freed by a read was not reusable")
	}
	p.Close()
	if p.Write([]byte("z")) {
		t.Fatal("write after close accepted")
	}
	rest, ok := p.Read(64, nil)
	if !ok || string(rest) != "fghijk" {
		t.Fatalf("queued bytes lost at close: %q", rest)
	}
	if _, ok = p.Read(64, nil); ok {
		t.Fatal("closed, drained pipe still readable")
	}
}

func TestOutputPipeReadWaitsAndAbortWakes(t *testing.T) {
	p := NewOutputPipe(64)
	got := make(chan []byte, 1)
	go func() { data, _ := p.Read(64, nil); got <- data }()
	time.Sleep(20 * time.Millisecond)
	p.Write([]byte("late"))
	select {
	case data := <-got:
		if !bytes.Equal(data, []byte("late")) {
			t.Fatal(string(data))
		}
	case <-time.After(time.Second):
		t.Fatal("reader missed a write")
	}
	stopped := make(chan bool, 1)
	go func() { _, ok := p.Read(64, nil); stopped <- ok }()
	time.Sleep(20 * time.Millisecond)
	p.Abort()
	select {
	case ok := <-stopped:
		if ok {
			t.Fatal("aborted read returned data")
		}
	case <-time.After(time.Second):
		t.Fatal("abort did not wake the reader")
	}
	stop := make(chan struct{})
	close(stop)
	if _, ok := NewOutputPipe(1).Read(1, stop); ok {
		t.Fatal("stop did not end the read")
	}
}
