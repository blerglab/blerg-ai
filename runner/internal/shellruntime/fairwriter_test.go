package shellruntime

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestFairWriterControlPriorityAndAgentFileProgress(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var order []string
	writer := NewFairWriter(func(_ context.Context, raw []byte) error {
		value := string(raw)
		if value == `{"type":"first"}` {
			close(entered)
			<-release
		}
		mu.Lock()
		order = append(order, value)
		mu.Unlock()
		return nil
	})
	defer writer.Close()
	var wg sync.WaitGroup
	send := func(data string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writer.Send(t.Context(), []byte(data)); err != nil {
				t.Error(err)
			}
		}()
	}
	send(`{"type":"first"}`)
	<-entered
	for range 12 {
		send(`{"type":"owner_shell_result","op":"renew"}`)
	}
	send(`{"type":"agent_event"}`)
	send(`{"type":"workspace_file_result"}`)
	send(`{"type":"owner_shell_output"}`)
	// Wait on queue state, not a timing window, before releasing the first write.
	for len(writer.control) != 12 || len(writer.ordinary) != 2 || len(writer.shell) != 1 {
		select {
		case <-t.Context().Done():
			t.Fatal("queues not filled")
		default:
			runtime.Gosched()
		}
	}
	close(release)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(order[1], "owner_shell_result") {
		t.Fatal("control not prioritized")
	}
	ordinary, shell := 99, 99
	for n, value := range order {
		if strings.Contains(value, "agent_event") || strings.Contains(value, "workspace_file_result") {
			ordinary = min(ordinary, n)
		}
		if strings.Contains(value, "owner_shell_output") {
			shell = n
		}
	}
	if ordinary > 10 || shell > 10 {
		t.Fatalf("fairness: ordinary=%d shell=%d", ordinary, shell)
	}
}
