package server

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveDataMarketProcessFixture(t *testing.T) {
	if os.Getenv("BLERG_LIVE_PROCESS_MARKET") != "1" {
		t.Skip("simulated market process only")
	}
	fd := os.NewFile(3, "simulated-market-listener")
	listener, e := net.FileListener(fd)
	_ = fd.Close()
	if e != nil {
		t.Fatal("market listener")
	}
	var mode atomic.Value
	mode.Store("good")
	var calls, lists atomic.Int64
	var mu sync.Mutex
	entered, release := make(chan struct{}, 1), make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		if r.URL.Path != "/mcp" || r.Header.Get("Authorization") != "Bearer process-provider-canary" {
			w.WriteHeader(403)
			return
		}
		var request struct {
			Method string
			ID     json.RawMessage
			Params struct {
				Name      string
				Arguments json.RawMessage
			}
		}
		if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		selected, ok := mode.Load().(string)
		if !ok {
			t.Error("fixture market mode")
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		reply := func(value any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": value})
		}
		switch request.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "simulated market MCP", "version": "1"}})
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			lists.Add(1)
			if selected == "listing outage" {
				w.WriteHeader(503)
				return
			}
			tools := []any{}
			if selected != "missing tool" {
				description := "Simulated market quotes"
				if selected == "changed hash" {
					description = "Changed simulated market quotes"
				}
				tools = append(tools, map[string]any{"name": "quotes", "description": description, "inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"symbol":{"type":"string"}},"required":["symbol"]}`), "annotations": map[string]bool{"readOnlyHint": selected != "not read only"}})
			}
			if selected == "duplicate tool" && len(tools) > 0 {
				tools = append(tools, tools[0])
			}
			reply(map[string]any{"tools": tools})
		case "tools/call":
			calls.Add(1)
			if request.Params.Name != "quotes" || string(request.Params.Arguments) != `{"symbol":"NVDA"}` {
				w.WriteHeader(403)
				return
			}
			if selected == "hold" {
				mu.Lock()
				signal, unblock := entered, release
				mu.Unlock()
				select {
				case signal <- struct{}{}:
				default:
				}
				select {
				case <-unblock:
				case <-r.Context().Done():
					return
				}
			}
			if selected == "auth refused" {
				w.WriteHeader(401)
				return
			}
			if selected == "outage" {
				w.WriteHeader(503)
				return
			}
			if selected == "rate limited" {
				w.Header().Set("Retry-After", "90")
				w.WriteHeader(429)
				return
			}
			var content any = map[string]any{"quotes": []any{map[string]any{"close": 185.25}}}
			switch selected {
			case "missing value":
				content = map[string]any{"quotes": []any{map[string]any{"close": 185.25}, map[string]any{"other": 1}}}
			case "null value":
				content = map[string]any{"quotes": []any{map[string]any{"close": nil}}}
			case "too many rows":
				rows := make([]any, 10001)
				for i := range rows {
					rows[i] = map[string]any{"close": float64(i)}
				}
				content = map[string]any{"quotes": rows}
			case "oversized":
				content = map[string]any{"quotes": []any{map[string]any{"close": 185.25}}, "padding": strings.Repeat("x", 6*1024*1024)}
			}
			reply(map[string]any{"structuredContent": content, "isError": false})
		default:
			w.WriteHeader(400)
		}
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	out, in := json.NewEncoder(os.Stdout), json.NewDecoder(os.Stdin)
	if out.Encode(liveProcessReply{Ready: true}) != nil {
		t.Fatal("market ready")
	}
	for {
		var command liveProcessCommand
		if e = in.Decode(&command); errors.Is(e, io.EOF) {
			return
		}
		if e != nil {
			t.Fatal("market fixture input")
		}
		switch command.Action {
		case "mode":
			mode.Store(command.Mode)
			mu.Lock()
			entered, release = make(chan struct{}, 1), make(chan struct{})
			mu.Unlock()
		case "await call":
			mu.Lock()
			signal := entered
			mu.Unlock()
			select {
			case <-signal:
			case <-time.After(10 * time.Second):
				if out.Encode(liveProcessReply{Error: "fixture_call_not_reached"}) != nil {
					t.Fatal("market fixture response")
				}
				continue
			}
		case "release":
			mu.Lock()
			select {
			case <-release:
			default:
				close(release)
			}
			mu.Unlock()
		case "counts":
		default:
			t.Fatal("market fixture action")
		}
		if out.Encode(liveProcessReply{Calls: int(calls.Load()), Lists: int(lists.Load())}) != nil {
			t.Fatal("market fixture response")
		}
	}
}
