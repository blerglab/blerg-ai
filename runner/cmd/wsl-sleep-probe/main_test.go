package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeClock struct {
	samples []sample
	next    int
	cancel  context.CancelFunc
	err     error
}

func (c *fakeClock) sample() (sample, error) {
	if c.err != nil {
		return sample{}, c.err
	}
	s := c.samples[c.next]
	c.next++
	return s, nil
}
func (c *fakeClock) wait(ctx context.Context) error {
	if c.next == len(c.samples) {
		c.cancel()
		return ctx.Err()
	}
	return nil
}

type capture struct {
	bytes.Buffer
	syncs             int
	writeErr, syncErr error
}

func (w *capture) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.Buffer.Write(p)
}
func (w *capture) Sync() error { w.syncs++; return w.syncErr }

func TestRecordAndReadFakeClock(t *testing.T) {
	for _, tc := range []struct {
		name             string
		wall, boot, mono time.Duration
		want             string
	}{
		{"awake", time.Second, time.Second, time.Second, "1s\t1s\t1s\t0s\t0s"},
		{"boottime includes sleep", 61 * time.Second, 61 * time.Second, time.Second, "1m1s\t1m1s\t1s\t1m0s\t0s"},
		{"guest clocks miss sleep", 61 * time.Second, time.Second, time.Second, "1m1s\t1s\t1s\t0s\t1m0s"},
		{"all clocks advance", 61 * time.Second, 61 * time.Second, 61 * time.Second, "1m1s\t1m1s\t1m1s\t0s\t0s"},
		{"wall correction", -time.Second, time.Second, time.Second, "-1s\t1s\t1s\t0s\t-2s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := sample{Wall: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), BootNS: int64(time.Hour), MonoNS: int64(time.Hour)}
			end := sample{Wall: start.Wall.Add(tc.wall), BootNS: start.BootNS + int64(tc.boot), MonoNS: start.MonoNS + int64(tc.mono)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := &fakeClock{samples: []sample{start, end}, cancel: cancel}
			var log capture
			if err := record(ctx, c, &log); !errors.Is(err, context.Canceled) {
				t.Fatalf("record: %v", err)
			}
			if log.syncs != 2 {
				t.Fatalf("syncs = %d", log.syncs)
			}
			var out bytes.Buffer
			if err := report(&log.Buffer, &out); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 2 || !strings.HasSuffix(lines[1], "\t"+tc.want) {
				t.Fatalf("report: %s", out.String())
			}
		})
	}
}

func TestRecordErrors(t *testing.T) {
	failure := errors.New("injected failure")
	for _, tc := range []struct {
		name                        string
		clockErr, writeErr, syncErr error
	}{
		{"clock", failure, nil, nil}, {"write", nil, failure, nil}, {"sync", nil, nil, failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClock{samples: []sample{{}}, err: tc.clockErr}
			w := &capture{writeErr: tc.writeErr, syncErr: tc.syncErr}
			if err := record(context.Background(), c, w); !errors.Is(err, failure) {
				t.Fatalf("got %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var w capture
	if err := record(ctx, &fakeClock{}, &w); !errors.Is(err, context.Canceled) || w.Len() != 0 {
		t.Fatalf("canceled record: %v", err)
	}
}

func TestReportRejectsInvalidRecordings(t *testing.T) {
	first := sample{Version: 1, Sequence: 1, Wall: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), BootNS: 100, MonoNS: 100}
	encode := func(s sample) string {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	for _, tc := range []struct{ name, data string }{
		{"empty", ""}, {"malformed", "{"}, {"truncated tail", encode(first) + "{"},
		{"missing clocks", `{"version":1,"sequence":1,"wall":"2026-01-01T00:00:00Z"}`},
		{"concatenated runs", encode(first) + encode(first)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := report(strings.NewReader(tc.data), io.Discard); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	for _, field := range []string{"version", "sequence", "wall", "boot", "mono"} {
		t.Run(field, func(t *testing.T) {
			next := first
			next.Sequence = 2
			switch field {
			case "version":
				next.Version = 2
			case "sequence":
				next.Sequence = 3
			case "wall":
				next.Wall = time.Time{}
			case "boot":
				next.BootNS--
			case "mono":
				next.MonoNS--
			}
			if err := report(strings.NewReader(encode(first)+encode(next)), io.Discard); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
}

func TestReportOutputError(t *testing.T) {
	failure := errors.New("write failure")
	if err := report(strings.NewReader(""), &capture{writeErr: failure}); !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
}

func TestRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	path := filepath.Join(t.TempDir(), "samples.jsonl")
	if err := os.WriteFile(path, []byte("existing evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"record", "-out", path}, io.Discard); err == nil {
		t.Fatal("overwrote existing file")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing evidence" {
		t.Fatalf("existing evidence changed: %v", err)
	}
	for _, args := range [][]string{nil, {"unknown"}, {"record"}, {"record", "-unknown"}, {"record", "-out", path, "extra"}, {"read"}, {"read", path, "extra"}, {"read", path + ".missing"}, {"read", path}} {
		if err := run(ctx, args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	valid := filepath.Join(t.TempDir(), "valid.jsonl")
	if err := os.WriteFile(valid, []byte(`{"version":1,"sequence":1,"wall":"2026-01-01T00:00:00Z","boottime_ns":100,"monotonic_ns":100}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(ctx, []string{"read", valid}, io.Discard); err == nil {
		t.Fatal(err)
	}
}
