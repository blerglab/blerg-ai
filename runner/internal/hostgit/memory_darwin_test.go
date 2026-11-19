package hostgit

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestDarwinNativeGroupRSS exercises the real Darwin ABI and helper aggregation.
// CI runs this under -race alongside TestWorkspaceSearchLargeIndexUnderContention.
func TestDarwinNativeGroupRSS(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30 & sleep 30 & echo ready; wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { KillGroup(cmd); _ = cmd.Wait() }()
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("group startup: %q %v", line, err)
	}
	members := 0
	rss, err := nativeGroupRSS(ctx, cmd.Process.Pid, func(call, pid, flavor int, buf []byte) (int, error) {
		n, err := darwinProcInfo(call, pid, flavor, buf)
		if call == procInfoPIDInfo && err == nil {
			members++
		}
		return n, err
	})
	if err != nil || rss == 0 || members < 3 {
		t.Fatalf("group RSS=%d members=%d err=%v", rss, members, err)
	}
	if rss, err := darwinGroupRSS(ctx, cmd.Process.Pid); err != nil || rss == 0 {
		t.Fatalf("production sample RSS=%d err=%v", rss, err)
	}
}

// BenchmarkDarwinGroupRSS compares the replaced subprocess with native sampling
// on the same live group. Run with -race to match the macOS security gate.
func BenchmarkDarwinGroupRSS(b *testing.B) {
	cmd := exec.Command("/bin/sleep", "300")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		b.Fatal(err)
	}
	defer func() { KillGroup(cmd); _ = cmd.Wait() }()
	b.Run("legacy-ps", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ctx, cancel := context.WithTimeout(b.Context(), memorySampleTimeout)
			ps := exec.CommandContext(ctx, "/bin/ps", "-o", "rss=", "-g", strconv.Itoa(cmd.Process.Pid))
			ps.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
			ps.WaitDelay = memorySampleTimeout
			out, err := ps.Output()
			cancel()
			if err != nil || len(out) == 0 {
				b.Fatalf("legacy sample: %q %v", out, err)
			}
		}
	})
	b.Run("native", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if rss, err := darwinGroupRSS(b.Context(), cmd.Process.Pid); err != nil || rss == 0 {
				b.Fatalf("native sample: RSS=%d %v", rss, err)
			}
		}
	})
}
