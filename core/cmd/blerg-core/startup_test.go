package main

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/testsupport"
)

func TestDatabaseStartupWaitsThroughOutage(t *testing.T) {
	var config net.ListenConfig
	listener, err := config.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	pool, err := openDatabase(ctx, "postgres://fixture@"+address+"/fixture?sslmode=disable", nil)
	if pool != nil {
		pool.Close()
		t.Fatal("opened unavailable database")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exited instead of waiting: %v", err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatal("did not wait through outage")
	}
}

func TestDatabaseStartupAppliesMigrationsBeforeReturning(t *testing.T) {
	fixture := testsupport.Pool(t, nil)
	if err := db.Ready(t.Context(), fixture); err == nil {
		t.Fatal("fixture already migrated")
	}
	pool, err := openDatabase(t.Context(), fixture.Config().ConnString(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.Ready(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseStartupRejectsInvalidConfigWithoutCredentials(t *testing.T) {
	_, err := openDatabase(t.Context(), "postgres://fixture:private-fixture-value@example.com/fixture?connect_timeout=invalid", nil)
	if err == nil || strings.Contains(err.Error(), "private-fixture-value") {
		t.Fatalf("unsafe configuration error: %v", err)
	}
}

// A permanent failure (here a wrong password) is logged with its reason each attempt, and the
// password is never printed.
func TestDatabaseStartupLogsWhyWithoutCredentials(t *testing.T) {
	fixture := testsupport.Pool(t, nil)
	dsn, err := url.Parse(fixture.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	const wrong = "wrong-Fixture-Password-7"
	dsn.User = url.UserPassword(dsn.User.Username(), wrong)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	ctx, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	pool, err := openDatabase(ctx, dsn.String(), nil)
	log.SetOutput(os.Stderr)
	if pool != nil {
		pool.Close()
		t.Fatal("opened a database with a wrong password")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	out := logs.String()
	if strings.Contains(out, wrong) || strings.Contains(out, dsn.String()) {
		t.Fatalf("log contains the credential:\n%s", out)
	}
	if !strings.Contains(out, "attempt 1 failed (SQLSTATE 28P01") || !strings.Contains(out, "password authentication failed") {
		t.Fatalf("log does not say why:\n%s", out)
	}
}
