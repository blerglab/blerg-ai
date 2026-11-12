package shellruntime

import (
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sync"
	"testing"
)

func TestPrivilegedOpenIncludesSuspendedCustodyUntilProvenClosed(t *testing.T) {
	service := NewService(func(RPC) error { return nil })
	child := &retainedChild{custody: protocol.ShellCustody{Privileged: true}}
	service.launches["session"] = &registered{retained: &retainedCoordinator{mu: new(sync.Mutex), children: map[string]*retainedChild{"one": child}}}
	if open, known := service.PrivilegedShellOpen("session"); !open || !known {
		t.Fatal("suspended privileged custody was hidden")
	}
	child.closed = true
	if open, known := service.PrivilegedShellOpen("session"); open || !known {
		t.Fatal("proven closed custody still open")
	}
	if open, known := service.PrivilegedShellOpen("missing"); open || !known {
		t.Fatal("missing launch has privileged shell")
	}
}
