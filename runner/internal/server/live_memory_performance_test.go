package server

import (
	"context"
	"net/http"
	"runtime"
	"sort"
	"testing"
	"time"
)

// Measurement, not a wall-clock leak guarantee: a held 100k-event session is
// polled repeatedly in one process, with GC-normalized heap samples. Fixtures
// use a real isolated database and the authenticated HTTP handler boundary.
func TestLivePollingRetainedHeap(t *testing.T) {
	fx := newLiveFx(t)
	_, err := fx.pool.Exec(context.Background(), `INSERT INTO agent_events(session_id,seq,client_event_id,kind,payload)
 SELECT $1,g,gen_random_uuid(),'assistant_text',jsonb_build_object('text','Finished task '||g,'done',true)
 FROM generate_series(1,100000) g`, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	token := fx.token(topicOwner, "human")
	read := func() time.Duration {
		start := time.Now()
		response := fx.getLive(fx.sess, token, "")
		if response.Code != http.StatusOK {
			t.Fatalf("live read status: %d", response.Code)
		}
		return time.Since(start)
	}
	for i := 0; i < 3; i++ {
		read()
	}
	for batch := 0; batch <= 5; batch++ {
		var durations []time.Duration
		if batch > 0 {
			for i := 0; i < 10; i++ {
				durations = append(durations, read())
			}
		}
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		var median time.Duration
		if len(durations) > 0 {
			median = durations[len(durations)/2]
		}
		t.Logf("polls=%d heap_alloc=%d heap_objects=%d goroutines=%d batch_median=%s", batch*10, stats.HeapAlloc, stats.HeapObjects, runtime.NumGoroutine(), median)
	}
	runtime.KeepAlive(fx)
}
