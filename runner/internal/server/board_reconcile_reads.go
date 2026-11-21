package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"sync"
)

type boardReconcileReadKey struct{}
type boardReconcileReads struct {
	mu     sync.Mutex
	values map[string]json.RawMessage
}

func boardReconcileContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, boardReconcileReadKey{}, &boardReconcileReads{values: map[string]json.RawMessage{}})
}

// Authority is checked by the caller on every use, before this per-run cache.
// Successful writes invalidate the snapshot. Nothing is reused across runs.
func boardReconcileCall(ctx context.Context, caller sessionBoardCaller, call mcpgw.BoardCall) (json.RawMessage, error) {
	cache, _ := ctx.Value(boardReconcileReadKey{}).(*boardReconcileReads)
	cacheable := call.Tool == "blerg_card_search" || call.Tool == "blerg_column_list"
	key := call.BoardID + ":" + call.Tool + ":" + string(call.Arguments)
	if cache != nil && cacheable {
		cache.mu.Lock()
		raw, ok := cache.values[key]
		cache.mu.Unlock()
		if ok {
			return raw, nil
		}
	}
	raw, err := caller.CallBoard(ctx, call)
	if err != nil {
		return raw, err
	}
	if cache != nil {
		cache.mu.Lock()
		defer cache.mu.Unlock()
		if !call.ReadOnly {
			clear(cache.values)
		} else if cacheable && len(raw) <= 256<<10 && len(cache.values) < 128 && boardToolResult(raw, nil) == nil {
			cache.values[key] = raw
		}
	}
	return raw, nil
}
