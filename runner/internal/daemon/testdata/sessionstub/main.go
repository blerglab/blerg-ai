// sessionstub is a test-only engine workload. Its C companion supplies pthread
// and clone histories; Go supplies the runtime's os/exec vfork path.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 4 {
		panic("usage: sessionstub HISTORY-BINARY MODE MARKER")
	}
	mode, marker := os.Args[2], os.Args[3]
	switch mode {
	case "go-exec":
		for range 300 {
			if err := exec.Command("/bin/true").Run(); err != nil {
				panic(err)
			}
		}
	case "many-threads":
		var ready sync.WaitGroup
		ready.Add(160)
		for range 160 {
			go func() { runtime.LockOSThread(); ready.Done(); select {} }()
		}
		ready.Wait()
	case "group-kill":
		// Kills its own background shells as Claude Code does when a monitor expires or a tool
		// times out: each shell has its own process group, and the whole group gets SIGKILL.
		if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
			panic(err)
		}
		for n := 0; ; n++ {
			if _, err := os.Stat(marker + ".stop"); err == nil {
				return
			}
			cmd := exec.Command("/bin/sh", "-c", "while :; do /bin/true | /bin/cat; done | /bin/cat")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				panic(err)
			}
			time.Sleep(time.Duration(n%7) * time.Millisecond)
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			if err := os.WriteFile(marker+".beat", []byte(fmt.Sprint(n)), 0600); err != nil {
				panic(err)
			}
		}
	case "idle-exit":
		if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
			panic(err)
		}
		return
	default:
		cmd := exec.Command(os.Args[1], mode, marker)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			panic(err)
		}
		return
	}
	if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
		panic(err)
	}
	for n := 0; ; n++ {
		if _, err := os.Stat(marker + ".stop"); err == nil {
			return
		}
		if err := os.WriteFile(marker+".beat", []byte(fmt.Sprint(n)), 0600); err != nil {
			panic(err)
		}
		time.Sleep(time.Millisecond)
	}
}
