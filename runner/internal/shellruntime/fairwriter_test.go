package shellruntime

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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

type recordedWrites struct {
	mu     sync.Mutex
	frames []string
}

func (r *recordedWrites) add(frame string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, frame)
}

func (r *recordedWrites) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.frames...)
}

func indexedFrames(int) func(int) []byte {
	return func(n int) []byte { return []byte("chunk-" + strconv.Itoa(n)) }
}

func position(frames []string, want string) int {
	return slices.Index(frames, want)
}

// A file reply gives the writer back after every frame: a control frame sent
// while the transfer runs waits for at most the chunk already on the wire.
func TestFairWriterControlOvertakesAFileTransferBetweenFrames(t *testing.T) {
	var written recordedWrites
	writing, release := make(chan struct{}), make(chan struct{})
	writer := NewFairWriter(func(_ context.Context, raw []byte) error {
		if string(raw) == "chunk-1" {
			close(writing)
			<-release
		}
		written.add(string(raw))
		return nil
	})
	defer writer.Close()
	file := make(chan error, 1)
	go func() { file <- writer.SendFile(t.Context(), 6, indexedFrames(6), time.Time{}) }()
	<-writing
	var wg sync.WaitGroup
	for _, frame := range []string{`{"type":"owner_shell_result","op":"renew"}`, `{"type":"daemon_heartbeat"}`} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := writer.Send(t.Context(), []byte(frame)); err != nil {
				t.Error(err)
			}
		}()
	}
	for {
		control, _, ordinary := writer.Pending()
		if control == 1 && ordinary == 1 {
			break
		}
		runtime.Gosched()
	}
	close(release)
	wg.Wait()
	if err := <-file; err != nil {
		t.Fatal(err)
	}
	frames := written.snapshot()
	if len(frames) != 8 {
		t.Fatalf("frames: %v", frames)
	}
	for _, want := range []string{`{"type":"owner_shell_result","op":"renew"}`, `{"type":"daemon_heartbeat"}`} {
		if at := position(frames, want); at < 0 || at > position(frames, "chunk-2") {
			t.Fatalf("%s waited behind the transfer: %v", want, frames)
		}
	}
}

// Saturated control never lets a file frame in; ordinary traffic does, so a
// reply still finishes inside its deadline on a busy link.
func TestFairWriterFileProgressesBehindOrdinaryButNeverAheadOfControl(t *testing.T) {
	for _, kind := range []string{"control", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			var written recordedWrites
			entered, release := make(chan struct{}), make(chan struct{})
			writer := NewFairWriter(func(_ context.Context, raw []byte) error {
				if string(raw) == `{"type":"gate"}` {
					close(entered)
					<-release
					return nil
				}
				written.add(string(raw))
				return nil
			})
			defer writer.Close()
			gate := make(chan error, 1)
			go func() { gate <- writer.Send(t.Context(), []byte(`{"type":"gate"}`)) }()
			<-entered
			frame := `{"type":"agent_event"}`
			count := 40
			if kind == "control" {
				frame, count = `{"type":"owner_shell_result"}`, 16
			}
			var wg sync.WaitGroup
			for range count {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := writer.Send(t.Context(), []byte(frame)); err != nil {
						t.Error(err)
					}
				}()
			}
			file := make(chan error, 1)
			go func() { file <- writer.SendFile(t.Context(), 2, indexedFrames(2), time.Time{}) }()
			for {
				control, _, ordinary := writer.Pending()
				if control+ordinary == count && len(writer.file) == 1 {
					break
				}
				runtime.Gosched()
			}
			close(release)
			wg.Wait()
			if err := <-file; err != nil {
				t.Fatal(err)
			}
			frames := written.snapshot()
			first := position(frames, "chunk-0")
			if kind == "control" && first != count {
				t.Fatalf("file frame at %d ran ahead of control: %v", first, frames)
			}
			if kind == "ordinary" && (first < 0 || first > fileTurnAfter) {
				t.Fatalf("file frame at %d starved behind ordinary traffic: %v", first, frames)
			}
		})
	}
}

// At most two file replies are held: the one writing and one waiting. A third
// waits outside the writer and gives up at its start bound.
func TestFairWriterHoldsAtMostTwoFileReplies(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	writer := NewFairWriter(func(_ context.Context, raw []byte) error {
		if string(raw) == "chunk-0" {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	})
	defer writer.Close()
	results := make(chan error, 2)
	go func() { results <- writer.SendFile(t.Context(), 1, indexedFrames(1), time.Time{}) }()
	<-entered
	go func() { results <- writer.SendFile(t.Context(), 1, indexedFrames(1), time.Time{}) }()
	for len(writer.file) != 1 {
		runtime.Gosched()
	}
	if err := writer.SendFile(t.Context(), 1, indexedFrames(1), time.Now().Add(30*time.Millisecond)); !errors.Is(err, ErrWriteExpired) {
		t.Fatalf("third reply: %v", err)
	}
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

// Past its start bound a queued reply is gone, and it is never written. One
// already started keeps going: the bound covers the wait, not the transfer.
func TestFairWriterStartByCoversOnlyTheWait(t *testing.T) {
	var written recordedWrites
	entered, release := make(chan struct{}), make(chan struct{})
	writer := NewFairWriter(func(_ context.Context, raw []byte) error {
		if string(raw) == `{"type":"gate"}` {
			close(entered)
			<-release
			return nil
		}
		time.Sleep(20 * time.Millisecond)
		written.add(string(raw))
		return nil
	})
	defer writer.Close()
	go func() { _ = writer.Send(t.Context(), []byte(`{"type":"gate"}`)) }()
	<-entered
	if err := writer.SendOrdinary(t.Context(), []byte("late"), time.Now().Add(30*time.Millisecond)); !errors.Is(err, ErrWriteExpired) {
		t.Fatalf("queued frame: %v", err)
	}
	close(release)
	if err := writer.SendFile(t.Context(), 4, indexedFrames(4), time.Now().Add(30*time.Millisecond)); err != nil {
		t.Fatalf("started transfer: %v", err)
	}
	if frames := written.snapshot(); len(frames) != 4 || position(frames, "late") >= 0 {
		t.Fatalf("frames: %v", frames)
	}
}

// Canceling between frames ends the reply there: nothing more is written and
// the writer serves the next frame.
func TestFairWriterCanceledFileStopsBetweenFrames(t *testing.T) {
	var written recordedWrites
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := NewFairWriter(func(_ context.Context, raw []byte) error {
		written.add(string(raw))
		if string(raw) == "chunk-1" {
			cancel()
		}
		return nil
	})
	defer writer.Close()
	if err := writer.SendFile(ctx, 6, indexedFrames(6), time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transfer: %v", err)
	}
	if err := writer.Send(t.Context(), []byte(`{"type":"agent_event"}`)); err != nil {
		t.Fatal(err)
	}
	frames := written.snapshot()
	if position(frames, "chunk-5") >= 0 || position(frames, `{"type":"agent_event"}`) < 0 {
		t.Fatalf("frames: %v", frames)
	}
}

// A failed frame ends the reply: no later chunk is written.
func TestFairWriterFailedFrameEndsTheTransfer(t *testing.T) {
	var written recordedWrites
	failure := errors.New("write failed")
	writer := NewFairWriter(func(_ context.Context, raw []byte) error {
		written.add(string(raw))
		if string(raw) == "chunk-1" {
			return failure
		}
		return nil
	})
	defer writer.Close()
	if err := writer.SendFile(t.Context(), 6, indexedFrames(6), time.Time{}); !errors.Is(err, failure) {
		t.Fatalf("failed transfer: %v", err)
	}
	if frames := written.snapshot(); len(frames) != 2 {
		t.Fatalf("frames after failure: %v", frames)
	}
}

// inChunk starts a transfer held inside its first chunk and returns what the
// test needs to steer it: the reply's result, its cancel and the chunk's release.
func inChunk(t *testing.T, writer func(func(context.Context, []byte) error) *FairWriter) (*FairWriter, <-chan error, context.CancelFunc, func()) {
	t.Helper()
	entered, held := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(held) }) }
	t.Cleanup(release)
	w := writer(func(_ context.Context, raw []byte) error {
		if string(raw) == "chunk-0" {
			close(entered)
			<-held
		}
		return nil
	})
	t.Cleanup(w.Close)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- w.SendFile(ctx, 6, indexedFrames(6), time.Time{}) }()
	<-entered
	return w, result, cancel, release
}

func replyOf(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the canceled reply never returned")
		return nil
	}
}

// The writer's end cannot strand a canceled reply: it returns whether the chunk
// on the wire finishes after the writer has gone or never finishes at all.
func TestFairWriterCanceledFileReturnsWhenTheWriterCloses(t *testing.T) {
	t.Run("chunk finishes after close", func(t *testing.T) {
		writer, result, cancel, release := inChunk(t, NewFairWriter)
		cancel()
		time.Sleep(20 * time.Millisecond)
		writer.Close()
		release()
		if err := replyOf(t, result); err == nil {
			t.Fatal("an unfinished reply reported success")
		}
	})
	t.Run("chunk still on the wire", func(t *testing.T) {
		writer, result, cancel, _ := inChunk(t, NewFairWriter)
		cancel()
		time.Sleep(20 * time.Millisecond)
		writer.Close()
		if err := replyOf(t, result); !errors.Is(err, ErrClosed) {
			t.Fatalf("reply: %v", err)
		}
	})
}

// A canceled reply ends with the chunk that was on the wire. It does not wait
// behind control frames for the writer's next file turn.
func TestFairWriterCanceledFileEndsAtTheChunkOnTheWire(t *testing.T) {
	holding, held := make(chan struct{}), make(chan struct{})
	defer close(held)
	writer, result, cancel, release := inChunk(t, func(write func(context.Context, []byte) error) *FairWriter {
		return NewFairWriter(func(ctx context.Context, raw []byte) error {
			if strings.Contains(string(raw), "owner_shell_result") {
				close(holding)
				<-held
				return nil
			}
			return write(ctx, raw)
		})
	})
	go func() { _ = writer.Send(context.Background(), []byte(`{"type":"owner_shell_result"}`)) }()
	for {
		if control, _, _ := writer.Pending(); control == 1 {
			break
		}
		runtime.Gosched()
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	release()
	<-holding
	if err := replyOf(t, result); !errors.Is(err, context.Canceled) {
		t.Fatalf("reply: %v", err)
	}
}
