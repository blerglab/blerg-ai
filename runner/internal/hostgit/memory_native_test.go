//go:build darwin || linux

package hostgit

import (
	"context"
	"encoding/binary"
	"errors"
	"syscall"
	"testing"
)

func TestNativeGroupRSS(t *testing.T) {
	for _, tc := range []struct {
		name             string
		listN            int
		listErr, taskErr error
		taskN            int
		want             uint64
		fail             bool
	}{
		{"sum", 8, nil, nil, 96, 301, false},
		{"empty", 0, nil, nil, 96, 0, false},
		{"departed", 8, nil, syscall.ESRCH, 0, 0, false},
		{"list-denied", 0, syscall.EPERM, nil, 96, 0, true},
		{"list-truncated", 64 << 10, nil, nil, 96, 0, true},
		{"list-malformed", 3, nil, nil, 96, 0, true},
		{"task-denied", 8, nil, syscall.EPERM, 0, 0, true},
		{"task-empty", 8, nil, nil, 0, 0, true},
		{"task-short", 8, nil, nil, 95, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			rss, err := nativeGroupRSS(t.Context(), 123, func(call, pid, flavor int, buf []byte) (int, error) {
				calls++
				if call == 1 {
					if pid != 2 || flavor != 123 {
						t.Fatalf("wrong group selection: %d %d", pid, flavor)
					}
					binary.LittleEndian.PutUint32(buf, 10)
					binary.LittleEndian.PutUint32(buf[4:], 11)
					return tc.listN, tc.listErr
				}
				if call != 2 || flavor != 4 || (pid != 10 && pid != 11) {
					t.Fatalf("wrong task selection: %d %d %d", call, pid, flavor)
				}
				binary.LittleEndian.PutUint64(buf[8:], 150*1024+1)
				return tc.taskN, tc.taskErr
			})
			if (err != nil) != tc.fail || rss != tc.want {
				t.Fatalf("RSS=%d err=%v", rss, err)
			}
			if calls == 0 {
				t.Fatal("did not sample")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := nativeGroupRSS(ctx, 123, func(int, int, int, []byte) (int, error) { t.Fatal("sample after cancellation"); return 0, nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled sample: %v", err)
	}
}

func TestNativeGroupRSSNumericBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		pid  uint32
		rss  uint64
		fail bool
	}{
		{"zero-pid", 0, 1, true},
		{"negative-pid", 1 << 31, 1, true},
		{"overflow", 10, ^uint64(0), true},
		{"zero-rss", 10, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rss, err := nativeGroupRSS(t.Context(), 123, func(call, pid, flavor int, buf []byte) (int, error) {
				if call == procInfoListPIDs {
					binary.LittleEndian.PutUint32(buf, tc.pid)
					binary.LittleEndian.PutUint32(buf[4:], tc.pid+1)
					return 8, nil
				}
				binary.LittleEndian.PutUint64(buf[8:], tc.rss)
				return len(buf), nil
			})
			if (err != nil) != tc.fail || rss != 0 {
				t.Fatalf("RSS=%d err=%v", rss, err)
			}
		})
	}
}

func TestNativeGroupRSSCancellationDuringSample(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := nativeGroupRSS(ctx, 123, func(call, pid, flavor int, buf []byte) (int, error) {
		cancel()
		return 0, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled enumeration: %v", err)
	}
}
