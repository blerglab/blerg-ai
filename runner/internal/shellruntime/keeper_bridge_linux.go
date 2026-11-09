//go:build linux

package shellruntime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

type bridgePacket struct {
	Kind     string          `json:"kind"`
	ID       string          `json:"id,omitempty"`
	Method   string          `json:"method,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
	OK       bool            `json:"ok,omitempty"`
	Deadline int64           `json:"deadline,omitempty"`
	Cause    string          `json:"cause,omitempty"`
}
type failedOutput struct {
	Session string
	Epoch   uint64
}

// KeeperRuntime runs in the keeper process. Thus each existing per-shell
// helper's creating thread/Pdeathsig belongs to the keeper, without moving or
// weakening its ptrace reaper, identity monitor, ring or lease implementation.
type KeeperRuntime struct {
	service     *Service
	operation   sync.RWMutex
	mu          sync.Mutex
	current     *shellkeeper.Channel
	generation  string
	rebound     bool // operation owns attach/rebind; no RPC inherits the old access link
	outputSlots chan struct{}
	outputs     map[string]keeperOutput
	observed    atomic.Pointer[[]hostObservation]
}

func NewKeeperRuntime() *KeeperRuntime {
	k := &KeeperRuntime{outputSlots: make(chan struct{}, 16), outputs: map[string]keeperOutput{}}
	k.service = NewService(k.send)
	k.service.SetOutputSender(k.sendOutput)
	return k
}
func (k *KeeperRuntime) send(r RPC) error {
	if r.Type == "owner_shell_output" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return k.sendOutput(ctx, r)
	}
	k.mu.Lock()
	ch := k.current
	k.mu.Unlock()
	if ch == nil {
		return bridgeFailure("detached", ErrClosed)
	}
	body, err := json.Marshal(r)
	if err != nil {
		return bridgeFailure("event_encode", err)
	}
	return ch.Send(bridgePacket{Kind: "event", Body: body})
}
func (k *KeeperRuntime) Serve(ctx context.Context, ch *shellkeeper.Channel) error {
	k.operation.Lock()
	k.mu.Lock()
	k.current = nil
	k.rebound = false
	if k.generation == "" {
		k.generation = ch.Generation()
		k.service.mu.Lock()
		k.service.persistentDaemon = k.generation
		k.service.mu.Unlock()
	}
	same := k.generation == ch.Generation()
	k.mu.Unlock()
	if !same {
		k.operation.Unlock()
		return bridgeFailure("generation", ErrClosed)
	}
	// Join old output and suspend I/O before the next authority can rekey.
	suspendCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err := k.service.AcceptShellLink(suspendCtx, k.service.ShellHello(), nil)
	cancel()
	if err == nil {
		k.mu.Lock()
		k.current = ch
		k.mu.Unlock()
	}
	k.operation.Unlock()
	if err != nil {
		return bridgeFailure("suspend", err)
	}
	defer func() {
		k.operation.Lock()
		defer k.operation.Unlock()
		k.mu.Lock()
		own := k.current == ch
		if own {
			k.current = nil
		}
		k.mu.Unlock()
		if own {
			call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			if err := k.service.Rebind(call); err != nil {
				_ = bridgeFailure("detach", err)
			}
		}
	}()
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()
	requests := make(chan bridgePacket, 64)
	heartbeats := make(chan bridgePacket, 1)
	heartbeatFinished := make(chan struct{})
	go func() {
		defer close(heartbeatFinished)
		for {
			select {
			case <-streamCtx.Done():
				return
			case packet := <-heartbeats:
				if err := k.serveRequest(streamCtx, ch, packet); err != nil {
					_ = ch.Close()
					return
				}
			}
		}
	}()
	readDone := make(chan error, 1)
	readFinished := make(chan struct{})
	go func() {
		defer close(readFinished)
		for {
			var packet bridgePacket
			if err := ch.Receive(&packet); err != nil {
				readDone <- err
				return
			}
			if packet.Kind == "output_ack" {
				k.acknowledge(ch, packet)
				continue
			}
			if packet.Method == "heartbeat" {
				select {
				case heartbeats <- packet:
				case <-streamCtx.Done():
					return
				default:
					if ch.SendContext(streamCtx, bridgePacket{Kind: "reply", ID: packet.ID}) != nil {
						readDone <- ErrClosed
						return
					}
				}
				continue
			}
			select {
			case requests <- packet:
			case <-streamCtx.Done():
				return
			default:
				_ = bridgeFailure("request_backpressure", ErrClosed)
				if ch.SendContext(streamCtx, bridgePacket{Kind: "reply", ID: packet.ID}) != nil {
					readDone <- ErrClosed
					return
				}
			}
		}
	}()
	var calls sync.WaitGroup
	slots := make(chan struct{}, 16)
	defer func() {
		streamCancel()
		_ = ch.Close()
		k.mu.Lock()
		for _, output := range k.outputs {
			if output.channel == ch {
				select {
				case output.result <- false:
				default:
				}
			}
		}
		k.mu.Unlock()
		calls.Wait()
		<-readFinished
		<-heartbeatFinished
	}()
	for {
		select {
		case err := <-readDone:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case packet := <-requests:
			select {
			case slots <- struct{}{}:
			case err := <-readDone:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
			calls.Add(1)
			go func() {
				defer calls.Done()
				defer func() { <-slots }()
				if err := k.serveRequest(streamCtx, ch, packet); err != nil {
					_ = ch.Close()
				}
			}()
		}
	}
}

func (k *KeeperRuntime) acknowledge(ch *shellkeeper.Channel, packet bridgePacket) {
	k.mu.Lock()
	defer k.mu.Unlock()
	pending, ok := k.outputs[packet.ID]
	if ok && pending.channel == ch {
		select {
		case pending.result <- packet.OK:
		default:
		}
	}
}

func (k *KeeperRuntime) serveRequest(ctx context.Context, ch *shellkeeper.Channel, packet bridgePacket) error {
	until := time.Now().Add(2 * time.Second)
	if packet.Deadline > 0 && time.Unix(0, packet.Deadline).Before(until) {
		until = time.Unix(0, packet.Deadline)
	}
	call, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	notification := packet.Kind == "notify" && packet.Method == "lost" && packet.ID == ""
	if !notification && (packet.Kind != "call" || packet.ID == "" || len(packet.ID) > 64) {
		return bridgeFailure("request_shape", ErrClosed)
	}
	// Liveness never queues behind a workspace operation or custody lock.
	if packet.Method != "heartbeat" {
		if packet.Method == "call" || packet.Method == "handle" {
			k.operation.RLock()
			defer k.operation.RUnlock()
		} else {
			k.operation.Lock()
			defer k.operation.Unlock()
		}
	}
	if call.Err() != nil {
		_ = bridgeFailure("request_expired", call.Err())
		return ch.SendContext(ctx, bridgePacket{Kind: "reply", ID: packet.ID, Cause: "call_deadline"})
	}
	k.mu.Lock()
	own := k.current == ch
	k.mu.Unlock()
	if !own || !ch.Current() {
		return bridgeFailure("stale_channel", ErrClosed)
	}
	if notification {
		var failed failedOutput
		if json.Unmarshal(packet.Body, &failed) != nil || failed.Session == "" {
			return bridgeFailure("transport_suspend_shape", ErrClosed)
		}
		if err := k.suspendOutput(call, failed); err != nil {
			return bridgeFailure("transport_suspend", err)
		}
		return nil
	}
	var body json.RawMessage
	var callErr error
	if packet.Method == "heartbeat" {
		body, callErr = json.Marshal(k.heartbeatSnapshot())
	} else {
		body, callErr = k.invoke(call, packet.Method, packet.Body)
	}
	if callErr != nil {
		_ = bridgeFailure("operation", callErr)
	}
	cause := ""
	if errors.Is(callErr, ErrAdoptionPending) {
		cause = "adoption_pending"
	} else if errors.Is(callErr, context.DeadlineExceeded) || errors.Is(callErr, context.Canceled) {
		cause = "call_deadline"
	}
	return ch.SendContext(ctx, bridgePacket{Kind: "reply", ID: packet.ID, Body: body, OK: callErr == nil, Cause: cause})
}

func (k *KeeperRuntime) suspendOutput(ctx context.Context, failed failedOutput) error {
	k.service.link.Lock()
	defer k.service.link.Unlock()
	k.service.mu.Lock()
	entry := k.service.launches[failed.Session]
	k.service.mu.Unlock()
	if entry == nil {
		_ = bridgeFailure("output_session_gone", ErrClosed)
		return nil
	}
	if c := entry.retained; c != nil {
		c.mu.Lock()
		same := c.highestEpoch == failed.Epoch
		c.mu.Unlock()
		if same {
			return c.Suspend(ctx)
		}
		return nil // an old failed delivery cannot suspend a newly committed epoch
	}
	if c := entry.coordinator; c != nil {
		return c.Notify(ctx, c.launch(), "paused")
	}
	return nil
}
func (k *KeeperRuntime) invoke(ctx context.Context, method string, body json.RawMessage) (json.RawMessage, error) {
	if !k.rebound && method != "hello" && method != "capabilities" && method != "rebind" && method != "probe" {
		return nil, ErrClosed
	}
	encode := func(value any, err error) (json.RawMessage, error) {
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(value)
		return data, err
	}
	session := func() (string, error) { var id string; err := json.Unmarshal(body, &id); return id, err }
	switch method {
	case "probe":
		var r hostRegistration
		if json.Unmarshal(body, &r) != nil || r.PublicRoot == "" || r.Root.Inode == 0 {
			return nil, ErrClosed
		}
		command := HostCommand("/proc/self/exe", r.Identity)
		command.Args = []string{"shell-supervisor", "--keeper-restart"}
		command.Root, command.PublicRoot = r.Root, r.PublicRoot
		_, err := ProbePersistentCommand(ctx, command)
		return nil, err
	case "hello":
		return encode(k.service.ShellHello(), nil)
	case "heartbeat":
		return encode(k.heartbeatSnapshot(), nil)
	case "link":
		var r hostLink
		if json.Unmarshal(body, &r) != nil {
			return nil, ErrClosed
		}
		valid := r.Welcome == nil || r.Hello == k.service.ShellHello() && r.Welcome.Matches(r.Hello, k.service.now())
		err := k.service.AcceptShellLink(ctx, r.Hello, r.Welcome)
		if !valid {
			return nil, ErrClosed
		}
		return nil, err
	case "rebind":
		err := k.service.Rebind(ctx)
		if err == nil {
			k.rebound = true
		}
		return nil, err
	case "capabilities":
		var r hostCapabilities
		if json.Unmarshal(body, &r) != nil {
			return nil, ErrClosed
		}
		k.service.SetCapabilities(r.Server, r.Runtime)
		return nil, nil
	case "register":
		var r hostRegistration
		if json.Unmarshal(body, &r) != nil {
			return nil, ErrClosed
		}
		return nil, k.register(ctx, r)
	case "binding", "workspace_binding":
		id, err := session()
		if err != nil {
			return nil, err
		}
		return encode(k.service.Binding(id))
	case "ready":
		id, err := session()
		if err != nil {
			return nil, err
		}
		return nil, k.ready(id)
	case "source":
		var source hostSource
		if json.Unmarshal(body, &source) != nil {
			return nil, ErrClosed
		}
		k.service.mu.Lock()
		entry := k.service.launches[source.Session]
		k.service.mu.Unlock()
		if entry != nil {
			entry.opMu.Lock()
			defer entry.opMu.Unlock()
			entry.command.SourceLive = func() bool { return source.Same && source.Known }
			entry.command.SourceState = func() (bool, bool) { return source.Same, source.Known }
			if !source.Same || !source.Known {
				entry.consentDirty.Store(true)
			}
		}
		if entry == nil {
			return nil, ErrClosed
		}
		if !source.Same || !source.Known {
			return nil, k.service.checkRetainedSource(ctx, entry)
		}
		return nil, nil
	case "handle", "call":
		var r RPC
		if json.Unmarshal(body, &r) != nil || r.SessionID == "" || len(r.Data) > ownershell.MaxFrame {
			return nil, ErrClosed
		}
		b, err := k.service.Binding(r.SessionID)
		if err != nil || b.Runtime != "host" {
			return nil, ErrClosed
		}
		if r.Op == "open" || r.Op == "admit" || r.Op == "freeze" || r.Op == "phase" && r.Phase == "syncing" {
			if err = k.ready(r.SessionID); err != nil {
				return nil, err
			}
		}
		if method == "call" {
			return encode(k.service.Call(ctx, r))
		}
		k.service.HandleContext(ctx, body)
		return nil, nil
	case "empty":
		id, err := session()
		if err != nil {
			return nil, err
		}
		return nil, k.service.ProveRecordedShellsEmpty(id)
	case "workspace_shells_open":
		id, err := session()
		if err != nil {
			return nil, err
		}
		// An authenticated negative proof is different from a failed request.
		// Agent membership may proceed on transport failure, while actual
		// recorded writers still prevent membership changes when observable.
		return encode(k.service.ProveRecordedShellsEmpty(id) != nil, nil)
	case "privileged":
		id, err := session()
		if err != nil {
			return nil, err
		}
		open, known := k.service.PrivilegedShellOpen(id)
		if !known {
			return nil, ErrKeeperUnavailable
		}
		return encode(open, nil)
	case "persistent":
		id, err := session()
		if err != nil {
			return nil, err
		}
		return encode(k.service.HasPersistentCustody(id), nil)
	case "process_phase":
		var r struct {
			Session string
			Freeze  bool
		}
		if json.Unmarshal(body, &r) != nil {
			return nil, ErrClosed
		}
		if err := k.ready(r.Session); err != nil {
			return nil, err
		}
		return nil, k.service.ProcessPhase(ctx, r.Session, r.Freeze)
	case "manifest":
		var r hostManifest
		if json.Unmarshal(body, &r) != nil {
			return nil, ErrClosed
		}
		return nil, k.service.UpdateManifest(r.Session, r.Old, r.Next)
	case "retire":
		var l protocol.SupervisorLaunch
		if json.Unmarshal(body, &l) != nil || l.Runtime != "host" {
			return nil, ErrClosed
		}
		return encode(k.service.Retire(ctx, l))
	case "retire_workspace":
		var r workspaceRetirement
		if json.Unmarshal(body, &r) != nil || r.Version != 1 || r.Workspace == "" || len(r.Workspace) > 1024 {
			return nil, ErrClosed
		}
		k.detachWorkspace(r.Workspace) //nolint:contextcheck // Accepted durable intent owns cleanup beyond an individual RPC.
		return nil, nil                // Acceptance, never a physical cleanup receipt.
	}
	return nil, ErrClosed
}
func (k *KeeperRuntime) register(ctx context.Context, r hostRegistration) error {
	b := r.Binding
	if b.Runtime != "host" || b.Container != "" || !b.Live || b.Cron || r.PublicRoot == "" || r.Root.Inode == 0 {
		return ErrClosed
	}
	k.service.mu.Lock()
	for e := range k.service.hostRetiring {
		if e.binding.Session == b.Session {
			k.service.mu.Unlock()
			return ErrAdoptionPending
		}
	}
	entry := k.service.launches[b.Session]
	k.service.mu.Unlock()
	if entry != nil {
		entry.opMu.Lock()
		locked := true
		defer func() {
			if locked {
				entry.opMu.Unlock()
			}
		}()
		k.service.mu.Lock()
		old := entry.binding
		command := entry.command
		k.service.mu.Unlock()
		if old.Owner != b.Owner || old.Launch != b.Launch || old.RuntimeGeneration != b.RuntimeGeneration || old.Root != b.Root || old.ManifestDigest != b.ManifestDigest || command.PublicRoot != r.PublicRoot || command.Root != r.Root || command.Identity.Cwd != r.Identity.Cwd {
			return ErrClosed
		}
		entry.mu.Lock()
		unavailable := entry.terminal || entry.fenced || entry.retiring
		entry.mu.Unlock()
		if unavailable {
			return ErrClosed
		}
		entry.mu.Lock()
		syncing := entry.phase == "syncing"
		entry.mu.Unlock()
		if syncing {
			// Exact runtime/root pins were re-proved by the new workspace owner.
			// Abandon its old sync freeze, but keep access suspended until rekey.
			locked = false
			entry.opMu.Unlock()
			binding, err := k.service.Binding(b.Session)
			if err != nil {
				return err
			}
			launch := ownershell.SupervisorIdentity(binding, ownershell.SignedGrant{}).Launch
			_, err = k.service.Call(ctx, RPC{SessionID: b.Session, Op: "phase", Phase: "running", Launch: launch})
			return err
		}
		// Existing custody keeps its cleanup keys, consent use, rings and sequence
		// state. Register never constructs a fresh empty replacement coordinator.
		return nil
	}
	k.service.mu.Lock()
	full := len(k.service.launches) >= 128
	k.service.mu.Unlock()
	if full {
		return ErrClosed
	}
	command := HostCommand("/proc/self/exe", r.Identity)
	command.Args = []string{"shell-supervisor", "--keeper-restart"}
	command.Root, command.PublicRoot = r.Root, r.PublicRoot
	return k.service.Register(ctx, new(sync.Mutex), b, command, func(context.Context) error { return k.service.ProveRecordedShellsEmpty(b.Session) })
}
func (k *KeeperRuntime) Inventory() []shellkeeper.Candidate {
	type census struct {
		session  string
		classes  []*retainedCoordinator
		ordinary []*Coordinator
		retired  bool
	}
	k.service.mu.Lock()
	var entries []census
	for id, e := range k.service.launches {
		classes := append([]*retainedCoordinator(nil), e.retainedHistory...)
		if e.retained != nil {
			classes = append(classes, e.retained)
		}
		ordinary := append([]*Coordinator(nil), e.containmentHistory...)
		if e.coordinator != nil {
			ordinary = append(ordinary, e.coordinator)
		}
		entries = append(entries, census{id, classes, ordinary, false})
	}
	for e := range k.service.hostRetiring {
		classes := append([]*retainedCoordinator(nil), e.retainedHistory...)
		if e.retained != nil {
			classes = append(classes, e.retained)
		}
		ordinary := append([]*Coordinator(nil), e.containmentHistory...)
		if e.coordinator != nil {
			ordinary = append(ordinary, e.coordinator)
		}
		entries = append(entries, census{e.binding.Session, classes, ordinary, true})
	}
	k.service.mu.Unlock()
	var result []shellkeeper.Candidate
	for _, entry := range entries {
		before := len(result)
		seenClasses := map[*retainedCoordinator]bool{}
		for _, c := range entry.classes {
			if seenClasses[c] {
				continue
			}
			seenClasses[c] = true
			c.mu.Lock()
			for id, child := range c.children {
				if !child.closed {
					result = append(result, shellkeeper.Candidate{Allocation: entry.session, Custody: id, Shell: child.custody.Shell})
				}
			}
			c.mu.Unlock()
		}
		seenOrdinary := map[*Coordinator]bool{}
		for _, c := range entry.ordinary {
			if seenOrdinary[c] {
				continue
			}
			seenOrdinary[c] = true
			c.sessionMu.Lock()
			seen := map[string]bool{}
			for id := range c.peers {
				result = append(result, shellkeeper.Candidate{Allocation: entry.session, Custody: "legacy:" + id, Shell: id})
				seen[id] = true
			}
			for id := range c.reserved {
				if !seen[id] {
					result = append(result, shellkeeper.Candidate{Allocation: entry.session, Custody: "legacy:" + id, Shell: id})
				}
			}
			c.sessionMu.Unlock()
		}
		if len(result) == before && !entry.retired {
			result = append(result, shellkeeper.EmptyLaunchCandidate(entry.session))
		}
	}
	return result
}
func (k *KeeperRuntime) ready(session string) error {
	if session == "" {
		return ErrClosed
	}
	k.service.link.RLock()
	defer k.service.link.RUnlock()
	if k.service.keeperDraining {
		return ErrClosed
	}
	k.service.mu.Lock()
	var classes []*retainedCoordinator
	for id, e := range k.service.launches {
		if id != session {
			continue
		}
		classes = append(classes, e.retainedHistory...)
		if e.retained != nil {
			classes = append(classes, e.retained)
		}
	}
	k.service.mu.Unlock()
	for _, c := range classes {
		c.mu.Lock()
		blocked := false
		for _, child := range c.children {
			if !child.closed && (!c.active || c.pending != nil || c.uncertain || child.closing != "") {
				blocked = true
			}
		}
		c.mu.Unlock()
		if blocked {
			return ErrAdoptionPending
		}
	}
	return nil
}
func (k *KeeperRuntime) Shutdown(ctx context.Context) error { return k.service.Shutdown(ctx) }

// BeginDrain closes actual execution admission and joins in-flight operations.
// An aborted/stale proof reopens it; a committed drain keeps it closed to exit.
func (k *KeeperRuntime) BeginDrain() (func(bool), error) {
	return k.beginDrain(k.Inventory)
}

func (k *KeeperRuntime) beginDrain(snapshot func() []shellkeeper.Candidate) (func(bool), error) {
	k.service.link.Lock()
	defer k.service.link.Unlock()
	if k.service.keeperDraining {
		return nil, ErrClosed
	}
	k.service.keeperDraining = true
	if len(shellkeeper.CustodyCandidates(snapshot())) != 0 {
		k.service.keeperDraining = false
		return nil, ErrClosed
	}
	return func(committed bool) {
		if !committed {
			k.service.link.Lock()
			k.service.keeperDraining = false
			k.service.link.Unlock()
		}
	}, nil
}

type keeperStream interface {
	Send(any) error
	Receive(any) error
	Close() error
}

type KeeperBridge struct {
	channel       keeperStream
	mu            sync.Mutex
	pending       map[string]chan bridgePacket
	pendingCalls  int
	sessions      map[string]bool
	blockedOutput map[string]uint64
	receiver      func(context.Context, RPC) error
	ctx           context.Context
	receiverReady chan struct{}
	receiverOnce  sync.Once
	done          chan struct{}
	once          sync.Once
	outputCtx     context.Context
	outputCancel  context.CancelFunc
	deliveries    map[string]*bridgeDelivery
	notices       map[string]RPC
}

func NewKeeperBridge(ctx context.Context, ch *shellkeeper.Channel, view shellkeeper.Snapshot) *KeeperBridge {
	return newKeeperBridge(ctx, ch, view)
}

func newKeeperBridge(ctx context.Context, ch keeperStream, view shellkeeper.Snapshot) *KeeperBridge {
	b := &KeeperBridge{channel: ch, pending: map[string]chan bridgePacket{}, sessions: map[string]bool{}, done: make(chan struct{}), receiverReady: make(chan struct{})}
	b.blockedOutput = map[string]uint64{}
	b.ctx = ctx
	b.outputCtx, b.outputCancel = context.WithCancel(ctx)
	b.deliveries = map[string]*bridgeDelivery{}
	b.notices = map[string]RPC{}
	for _, candidate := range view.Candidates {
		b.sessions[candidate.Allocation] = true
	}
	go b.read()
	return b
}
func ConnectKeeper(ctx context.Context, dir string, epoch shellkeeper.Epoch, generation string) (*KeeperBridge, error) {
	ready := make(chan *KeeperBridge, 1)
	ended := make(chan error, 1)
	go func() {
		ended <- shellkeeper.WithSession(ctx, dir, epoch, generation, func(ch *shellkeeper.Channel, view shellkeeper.Snapshot) error {
			bridge := NewKeeperBridge(ctx, ch, view)
			ready <- bridge
			// The authenticated transport owns context cancellation and closes
			// its socket; the reader then closes this bridge and joins the use.
			<-bridge.Done()
			return nil
		})
	}()
	select {
	case bridge := <-ready:
		return bridge, nil
	case err := <-ended:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (b *KeeperBridge) SetReceiver(receiver func(context.Context, RPC) error) {
	b.mu.Lock()
	if b.receiver != nil {
		b.mu.Unlock()
		_ = bridgeFailure("receiver_replaced", ErrClosed)
		_ = b.Close()
		return
	}
	b.receiver = receiver
	b.mu.Unlock()
	b.receiverOnce.Do(func() { close(b.receiverReady) })
}
func (b *KeeperBridge) HostSession(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[id]
}
func (b *KeeperBridge) Close() error {
	b.once.Do(func() { b.outputCancel(); close(b.done); _ = b.channel.Close() })
	return nil
}
func (b *KeeperBridge) Done() <-chan struct{} { return b.done }
func (b *KeeperBridge) read() {
	defer func() { _ = b.Close() }()
	output := make(chan *bridgeDelivery, 16)
	go b.deliverOutput(output)
	for {
		var p bridgePacket
		if b.channel.Receive(&p) != nil {
			return
		}
		if p.Kind == "reply" {
			b.mu.Lock()
			wait := b.pending[p.ID]
			b.mu.Unlock()
			if wait != nil {
				select {
				case wait <- p:
				default:
					_ = bridgeFailure("duplicate_reply", ErrClosed)
				}
			}
			continue
		}
		if p.Kind == "output_cancel" {
			b.mu.Lock()
			d := b.deliveries[p.ID]
			if d != nil {
				d.cancel()
			}
			b.mu.Unlock()
			continue
		}
		var r RPC
		if json.Unmarshal(p.Body, &r) != nil {
			_ = bridgeFailure("event_shape", ErrClosed)
			if b.refuseOutput(p) != nil {
				return
			}
			continue
		}
		if p.Kind == "output" && p.ID != "" && r.Type == "owner_shell_output" {
			ctx, cancel := context.WithCancel(b.outputCtx)
			d := &bridgeDelivery{id: p.ID, frame: r, ctx: ctx, cancel: cancel}
			b.mu.Lock()
			if len(b.deliveries) >= 16 || b.deliveries[p.ID] != nil {
				b.mu.Unlock()
				cancel()
				_ = bridgeFailure("output_window", ErrClosed)
				if b.refuseOutput(p) != nil {
					return
				}
				continue
			}
			b.deliveries[p.ID] = d
			notice := r
			notice.Data = nil
			b.notices[r.SessionID+"\x00"+r.Shell] = notice
			b.mu.Unlock()
			select {
			case output <- d:
			case <-b.done:
				return
			}
			continue
		}
		if p.Kind != "event" || r.Type == "owner_shell_output" {
			_ = bridgeFailure("event_kind", ErrClosed)
			if b.refuseOutput(p) != nil {
				return
			}
			continue
		}
		select {
		case <-b.receiverReady:
		case <-b.done:
			return
		}
		b.mu.Lock()
		receiver := b.receiver
		b.mu.Unlock()
		if receiver == nil || receiver(b.outputCtx, r) != nil {
			_ = bridgeFailure("event_delivery", ErrClosed)
		}
	}
}

// A refused frame that never began delivery is already joined. A duplicate
// ID must join the original delivery before acknowledging that same ID.
func (b *KeeperBridge) refuseOutput(p bridgePacket) error {
	if p.Kind != "output" || p.ID == "" {
		return nil
	}
	b.mu.Lock()
	if d := b.deliveries[p.ID]; d != nil {
		d.cancel()
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	return b.channel.Send(bridgePacket{Kind: "output_ack", ID: p.ID, OK: false})
}

func (b *KeeperBridge) Invoke(ctx context.Context, method string, in, out any) error {
	id, err := newHelperID()
	if err != nil {
		return bridgeFailure("request_id", err)
	}
	data, err := json.Marshal(in)
	if err != nil {
		return bridgeFailure("request_encode", err)
	}
	wait := make(chan bridgePacket, 1)
	b.mu.Lock()
	if method != "heartbeat" && b.pendingCalls >= 128 {
		b.mu.Unlock()
		_ = bridgeFailure("request_limit", ErrKeeperUnavailable)
		return ErrKeeperUnavailable
	}
	b.pending[id] = wait
	if method != "heartbeat" {
		b.pendingCalls++
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		if method != "heartbeat" {
			b.pendingCalls--
		}
		b.mu.Unlock()
	}()
	if sender, ok := b.channel.(interface {
		SendContext(context.Context, any) error
	}); ok {
		until := time.Now().Add(2 * time.Second)
		if d, ok := ctx.Deadline(); ok && d.Before(until) {
			until = d
		}
		err = sender.SendContext(ctx, bridgePacket{Kind: "call", ID: id, Method: method, Body: data, Deadline: until.UnixNano()})
	} else {
		err = b.channel.Send(bridgePacket{Kind: "call", ID: id, Method: method, Body: data})
	}
	if err != nil {
		if ctx.Err() != nil {
			_ = bridgeFailure("request_send_deadline", ctx.Err())
			return ctx.Err()
		}
		return err
	}
	select {
	case p := <-wait:
		if !p.OK {
			if p.Cause == "call_deadline" {
				_ = bridgeFailure("call_deadline", ErrKeeperUnavailable)
				return ErrKeeperUnavailable
			}
			if p.Cause == "adoption_pending" {
				_ = bridgeFailure("adoption_pending", ErrAdoptionPending)
				return ErrAdoptionPending
			}
			return bridgeFailure("refused", ErrClosed)
		}
		if out != nil && json.Unmarshal(p.Body, out) != nil {
			return bridgeFailure("reply_decode", ErrClosed)
		}
		if method == "register" {
			var r hostRegistration
			if json.Unmarshal(data, &r) == nil {
				b.mu.Lock()
				b.sessions[r.Binding.Session] = true
				b.mu.Unlock()
			}
		}
		if method == "retire" {
			var launch protocol.SupervisorLaunch
			if json.Unmarshal(data, &launch) == nil {
				b.mu.Lock()
				delete(b.sessions, launch.SessionID)
				for key, notice := range b.notices {
					if notice.SessionID == launch.SessionID {
						delete(b.notices, key)
					}
				}
				b.mu.Unlock()
			}
		}
		if method == "call" {
			var r RPC
			if json.Unmarshal(data, &r) == nil {
				if r.Op == "close" && r.Custody != nil {
					b.mu.Lock()
					delete(b.notices, r.Custody.Session+"\x00"+r.Custody.Shell)
					b.mu.Unlock()
				}
				if r.Op == "open" {
					b.rememberNotice(RPC{Version: r.Version, Type: "owner_shell_output", SessionID: r.SessionID, Shell: r.Grant.Grant.ShellGeneration, Custody: r.Custody, Epoch: r.Grant.Grant.AccessEpoch})
				}
				// Adopted shells can be silent indefinitely. Remember the signed
				// inventory and fresh grants before commit, not just first output.
				if r.Op == "prepare" && r.Inventory != nil {
					for _, claim := range r.Inventory.Entries {
						for _, grant := range r.Grants {
							if grant.Grant.CustodyID == claim.Custody.Digest() {
								custody := claim.Custody
								b.rememberNotice(RPC{Version: 2, Type: "owner_shell_output", SessionID: custody.Session, Shell: custody.Shell, Custody: &custody, Epoch: grant.Grant.AccessEpoch})
							}
						}
					}
				}
			}
		}
		return nil
	case <-ctx.Done():
		_ = bridgeFailure("request_deadline", ctx.Err())
		return ctx.Err()
	case <-b.done:
		return bridgeFailure("connection_lost", ErrClosed)
	}
}

func (b *KeeperBridge) rememberNotice(notice RPC) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := notice.SessionID + "\x00" + notice.Shell
	if old, ok := b.notices[key]; !ok || old.Epoch < notice.Epoch {
		b.notices[key] = notice
	}
}

// Metadata backs the established context-free getter API with the authenticated
// daemon/keeper session lifetime and its own bounded request deadline.
func (b *KeeperBridge) Metadata(method string, in, out any) error {
	ctx, cancel := context.WithTimeout(b.ctx, 5*time.Second)
	defer cancel()
	return b.Invoke(ctx, method, in, out)
}
