package server

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPermissionSignInCacheBoundAndTransientFailures(t *testing.T) {
	now := time.Now()
	c := permissionSignInCache{now: func() time.Time { return now }}
	calls := 0
	fetch := func(context.Context) (bool, bool) { calls++; return true, true }
	for i := range maxPermissionSignIns + 10 {
		c.load(t.Context(), fmt.Sprint(i), fetch)
	}
	if len(c.entries) > maxPermissionSignIns {
		t.Fatal("cache exceeded bound")
	}
	c.load(t.Context(), "last", fetch)
	c.load(t.Context(), "last", fetch)
	if calls != maxPermissionSignIns+11 {
		t.Fatal("result not cached")
	}
	now = now.Add(permissionSignInTTL)
	c.load(t.Context(), "last", fetch)
	if len(c.entries) != 1 {
		t.Fatal("expired entries retained")
	}
	failures := 0
	transient := func(context.Context) (bool, bool) { failures++; return true, false }
	c.load(t.Context(), "transient", transient)
	c.load(t.Context(), "transient", transient)
	if failures != 2 {
		t.Fatal("transient failure cached")
	}
}
