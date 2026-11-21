package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveDataStartupSyncRetriesAndRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ticks := make(chan time.Time, 2)
	var ready atomic.Bool
	var calls atomic.Int32
	syncCheckpoint := func(context.Context) error {
		n := calls.Add(1)
		if n == 1 {
			if ready.Load() {
				t.Error("failed startup enabled approvals")
			}
			return errors.New("fixture authority unavailable")
		}
		return nil
	}
	initializeIssuerReadiness(ctx, syncCheckpoint, &ready)
	if ready.Load() {
		t.Fatal("failed startup enabled approvals")
	}
	done := make(chan struct{})
	go func() { defer close(done); retryIssuerReadiness(ctx, syncCheckpoint, &ready, ticks) }()
	ticks <- time.Time{}
	<-done
	if !ready.Load() || calls.Load() != 2 {
		t.Fatal("startup did not recover", ready.Load(), calls.Load())
	}
}

func TestLiveDataApprovalWaitsForStartupRecovery(t *testing.T) {
	var ready, fenced atomic.Bool
	if got := issuerApprovalEpoch(7, &ready, &fenced); got != 0 {
		t.Fatal("startup failure admitted approvals", got)
	}
	ready.Store(true)
	if got := issuerApprovalEpoch(7, &ready, &fenced); got != 7 {
		t.Fatal("startup recovery left approvals off", got)
	}
	fenced.Store(true)
	if got := issuerApprovalEpoch(7, &ready, &fenced); got != 0 {
		t.Fatal("recovery bypassed epoch fence", got)
	}
}
