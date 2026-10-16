package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLockPoolSizeFollowsSessionCap(t *testing.T) {
	t.Cleanup(func() { lockPoolSessions.Store(nil) })
	for _, tc := range []struct{ sessions, want int }{{0, lockPoolFloor}, {1, lockPoolFloor}, {4, 10}, {10, 22}, {64, lockPoolCeiling}} {
		SetLockPoolSessions(func() int { return tc.sessions })
		if got := lockPoolSize(); int(got) != tc.want {
			t.Errorf("sessions %d: pool %d, want %d", tc.sessions, got, tc.want)
		}
	}
	lockPoolSessions.Store(nil)
	if got := lockPoolSize(); got != 10 {
		t.Errorf("default pool %d, want 10", got)
	}
}

// A fence that cannot get a pool connection in time is retried once instead
// of dropping the pod's socket.
func TestRuntimeLaunchFenceRetriesOncePoolTimeout(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	// A DSN of its own gives this test a fresh fence pool at the floor size.
	cfg, err := pgxpool.ParseConfig(dsn + sep + "application_name=fence_retry_test")
	if err != nil {
		t.Fatal(err)
	}
	main, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(main.Close)
	SetLockPoolSessions(func() int { return 0 })
	t.Cleanup(func() { lockPoolSessions.Store(nil) })
	ctx := context.Background()
	fence, err := runtimeFencePool(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	if fence.Config().MaxConns != lockPoolFloor {
		t.Fatalf("fence pool %d connections, want %d", fence.Config().MaxConns, lockPoolFloor)
	}
	// Hold every fence connection past the first attempt's 5 s deadline.
	var held []*pgxpool.Conn
	for i := 0; i < lockPoolFloor; i++ {
		c, err := fence.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	time.AfterFunc(5500*time.Millisecond, func() {
		defer wg.Done()
		for _, c := range held {
			c.Release()
		}
	})
	ran := 0
	err = WithRuntimeLaunchFenceRetry(ctx, main, "00000000-0000-4000-8000-0000000000ff", func(context.Context) error {
		ran++
		return nil
	})
	wg.Wait()
	if err != nil || ran != 1 {
		t.Fatalf("retry after a pool timeout: err=%v ran=%d", err, ran)
	}
	handlerErr := errors.New("handler refused")
	if err = WithRuntimeLaunchFenceRetry(ctx, main, "00000000-0000-4000-8000-0000000000fe", func(context.Context) error { ran++; return handlerErr }); !errors.Is(err, handlerErr) || ran != 2 {
		t.Fatalf("handler error was retried or masked: err=%v ran=%d", err, ran)
	}
}
