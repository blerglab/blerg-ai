//go:build linux

package shellkeeper

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"golang.org/x/sys/unix"
)

const wireLimit = 256 * 1024
const preAuthDeadline = 250 * time.Millisecond

var errBusy = errors.New("keeper temporarily busy")
var ErrRestartRequired = errors.New("keeper protocol incompatible: restart the shell keeper service and reopen shells; agents keep running")

func refusal(stage string, err error) error {
	cause := failurelog.Cause(err)
	if errors.Is(err, ErrRefused) {
		cause = stage // developer-owned proof failure, never peer-provided content
	}
	failurelog.Default.Event("shell_keeper."+stage, slog.LevelError, "component", "shell_keeper", "stage", stage, "cause", cause)
	return ErrRefused
}

// Server owns the flock and socket as one lifetime. Slice 2 deliberately has no
// launch, attach or I/O endpoint: inventory is observational; only a signed
// empty-keeper drain may mutate lifetime before slice 3.
type Server struct {
	listener   *net.UnixListener
	lock       *os.File
	identity   Identity
	generation string
	mu         sync.Mutex
	epoch      Epoch
	workers    sync.WaitGroup
	done       chan struct{}
	stop       context.CancelFunc
	snapshot   func() []Candidate // keeper-owned; never set from a peer request
	dir        string
	build      string
	draining   bool // guarded by mu; admission closes before the signed drain acknowledgment
	service    func(context.Context, *Channel) error
	ctx        context.Context
	active     *net.UnixConn
	retire     func(context.Context) error
	closeErr   error
	inventory  sync.Mutex
	drain      func() (func(bool), error)
}

func Listen(ctx context.Context, dir string) (*Server, error) {
	return ListenService(ctx, dir, nil, nil)
}

func ListenService(ctx context.Context, dir string, snapshot func() []Candidate, service func(context.Context, *Channel) error) (*Server, error) {
	return ListenRuntime(ctx, dir, snapshot, service, nil)
}

func ListenRuntime(ctx context.Context, dir string, snapshot func() []Candidate, service func(context.Context, *Channel) error, cleanup func() error) (*Server, error) {
	return ListenWithRuntime(ctx, dir, RuntimeHooks{Inventory: snapshot, Serve: service, Prepare: cleanup})
}

type RuntimeHooks struct {
	Inventory func() []Candidate
	Serve     func(context.Context, *Channel) error
	Prepare   func() error
	Retire    func(context.Context) error
	Drain     func() (func(bool), error)
}

func ListenWithRuntime(ctx context.Context, dir string, hooks RuntimeHooks) (*Server, error) {
	i, err := loadKeeper(dir)
	if err != nil {
		return nil, err
	}
	lock, err := acquireLock(dir, "keeper.lock")
	if err != nil {
		return nil, err
	}
	fail := func(stage string, err error) (*Server, error) {
		_ = lock.Close()
		return nil, refusal(stage, err)
	}
	if hooks.Prepare != nil {
		if err = hooks.Prepare(); err != nil {
			if errors.Is(err, ErrUnsupported) {
				_, _ = fail("unsupported_runtime", err)
				return nil, ErrUnsupported
			}
			return fail("orphan_cleanup", err)
		}
	}
	generation, err := nonce()
	if err != nil {
		return fail("generation", err)
	}
	epoch, err := loadEpochWithKey(dir, i.DaemonPublic)
	if err != nil {
		return fail("keeper_epoch", err)
	}
	build, err := BuildID()
	if err != nil {
		return fail("keeper_build", err)
	}
	path := filepath.Join(dir, "keeper.sock")
	if _, err = os.Lstat(path); err == nil {
		if err = staleSocketIdentity(path); err != nil {
			return fail("stale_socket_identity", err)
		}
		if err = os.Remove(path); err != nil {
			return fail("stale_socket_remove", err)
		}
	} else if !os.IsNotExist(err) {
		return fail("socket_stat", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return fail("socket_listen", err)
	}
	// The private directory denies access even before chmod. Go's listener
	// unlinks its socket on Close while this process still holds the flock.
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return fail("socket_mode", err)
	}
	serveCtx, stop := context.WithCancel(ctx)
	s := &Server{listener: listener, lock: lock, identity: i, generation: generation, epoch: epoch, dir: dir, build: build, done: make(chan struct{}), stop: stop, snapshot: func() []Candidate { return nil }}
	s.ctx, s.service, s.retire, s.drain = serveCtx, hooks.Serve, hooks.Retire, hooks.Drain
	if hooks.Inventory != nil {
		s.snapshot = hooks.Inventory
	}
	go s.serve(serveCtx)
	return s, nil
}

func (s *Server) Close() error {
	s.stop()
	<-s.done
	return s.closeErr
}

func (s *Server) Done() <-chan struct{} { return s.done }

func (s *Server) Drained() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

func (s *Server) serve(ctx context.Context) {
	defer close(s.done)
	defer func() {
		// A failed cleanup is not permission to publish an empty successor.
		// serveKeeper exits on this error; retain custody until process death.
		if s.closeErr == nil {
			_ = s.lock.Close()
		}
	}()
	go func() {
		<-ctx.Done()
		_ = s.listener.Close()
	}()
	s.acceptConnections(ctx, s.listener.AcceptUnix, waitRetry, s.hello)
	s.workers.Wait()
	if s.retire != nil {
		call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := s.retire(call); err != nil {
			s.closeErr = refusal("runtime_cleanup", err)
		}
	}
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Server) acceptConnections(ctx context.Context, accept func() (*net.UnixConn, error), pause func(context.Context, time.Duration) bool, handle func(*net.UnixConn) error) {
	backoff := 10 * time.Millisecond
	slots := make(chan struct{}, 16)
	for {
		conn, err := accept()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				_ = refusal("socket_accept", err)
				if !pause(ctx, backoff) {
					break
				}
				backoff = min(250*time.Millisecond, backoff*2)
				continue
			}
			if ctx.Err() == nil {
				_ = refusal("socket_accept", err)
				s.stop()
			}
			break
		}
		backoff = 10 * time.Millisecond
		select {
		case slots <- struct{}{}:
			s.workers.Add(1)
			go func() {
				defer s.workers.Done()
				defer func() { <-slots }()
				defer func() { _ = conn.Close() }()
				if err := handle(conn); err != nil {
					_ = refusal("hello_refused", err)
				}
			}()
		default:
			_ = refusal("connection_limit", ErrRefused)
			_ = conn.Close()
		}
	}
}

func socketIdentity(path string) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Mode&0777 != 0600 || st.Uid != uint32(os.Getuid()) { //nolint:gosec // kernel uid
		return ErrRefused
	}
	return nil
}

// Under the singleton flock, a crash-before-chmod socket may have any mode.
// Only a socket owned by this UID is removable; never follow or remove links.
func staleSocketIdentity(path string) error {
	var st unix.Stat_t
	if err := unix.Lstat(path, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Uid != uint32(os.Getuid()) { //nolint:gosec // kernel uid, never a narrowing of peer input
		return ErrRefused
	}
	return nil
}

func peerIdentity(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	var peer *unix.Ucred
	var peerErr error
	if err = raw.Control(func(fd uintptr) { peer, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return err
	}
	if peerErr != nil {
		return peerErr
	}
	if !validPeer(peer, uint32(os.Getuid())) { //nolint:gosec // kernel uid
		return ErrRefused
	}
	return nil
}

func validPeer(peer *unix.Ucred, uid uint32) bool {
	return peer != nil && peer.Pid > 0 && peer.Uid == uid
}

type wire struct {
	scan *bufio.Scanner
	out  io.Writer
}

func newWire(conn *net.UnixConn) *wire {
	return newWireIO(conn, conn)
}
func newWireIO(in io.Reader, out io.Writer) *wire {
	scan := bufio.NewScanner(in)
	scan.Buffer(make([]byte, 4096), wireLimit)
	return &wire{scan: scan, out: out}
}

func (w *wire) read() (message, error) {
	var m message
	if !w.scan.Scan() {
		if err := w.scan.Err(); err != nil {
			return m, err
		}
		return m, io.ErrUnexpectedEOF
	}
	dec := json.NewDecoder(bytes.NewReader(w.scan.Bytes()))
	dec.DisallowUnknownFields()
	if dec.Decode(&m) != nil || dec.Decode(new(any)) != io.EOF {
		return message{}, ErrRefused
	}
	return m, nil
}

func (w *wire) write(m message) error {
	data, err := json.Marshal(m)
	if err != nil || len(data)+1 >= wireLimit {
		return ErrRefused
	}
	_, err = w.out.Write(append(data, '\n'))
	return err
}

func (s *Server) hello(conn *net.UnixConn) error {
	if err := peerIdentity(conn); err != nil {
		return refusal("peer_uid", err)
	}
	if err := conn.SetDeadline(time.Now().Add(preAuthDeadline)); err != nil {
		return refusal("peer_deadline", err)
	}
	w := newWire(conn)
	request, err := w.read()
	if err != nil {
		return refusal("hello_decode", err)
	}
	p := request.Proof
	version := Version
	if p.Operation == "drain" || p.Operation == "update" {
		version = maintenanceVersion
	}
	if p.Operation != "" && p.Operation != "drain" && p.Operation != "update" && p.Operation != "service" {
		return refusal("hello_operation", ErrRefused)
	}
	if p.Version != version || len(p.ClientNonce) != 64 || len(p.ClientPublic) != 32 || len(p.ClientBuild) != 64 || len(p.KeeperPublic) != 0 || p.KeeperBuild != "" || p.ServerNonce != "" || p.Generation != "" || len(request.Signature) != 0 || len(request.MAC) != 0 || len(request.Candidates) != 0 {
		return refusal("hello_shape_version", ErrRefused)
	}
	s.mu.Lock()
	build := s.build
	s.mu.Unlock()
	public := s.identity.DaemonPublic
	if !validEpoch(public, p.Epoch) {
		return refusal("hello_epoch_signature", ErrRefused)
	}
	p.ServerNonce, err = nonce()
	if err != nil {
		return refusal("hello_nonce", err)
	}
	p.Generation = s.generation
	secret, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return refusal("keeper_ephemeral", err)
	}
	p.KeeperPublic, p.KeeperBuild = secret.PublicKey().Bytes(), build
	key, err := sessionKey(secret, p.ClientPublic, p)
	if err != nil {
		return refusal("keeper_session_key", err)
	}
	if err = w.write(message{Proof: p, Signature: sign(s.identity.Keeper, "keeper", p)}); err != nil {
		return refusal("challenge_write", err)
	}
	auth, err := w.read()
	if err != nil {
		return refusal("auth_decode", err)
	}
	if !reflect.DeepEqual(auth.Proof, p) || len(auth.Candidates) != 0 || len(auth.MAC) != 0 || !verify(public, "daemon", p, auth.Signature) {
		return refusal("auth_signature", ErrRefused)
	}
	// Verification and epoch order are one transaction. A late proof from an
	// older daemon cannot roll back a newer accepted authority.
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	if s.draining {
		return refusal("keeper_draining", ErrRefused)
	}
	if err = s.acceptEpoch(p); err != nil {
		return err
	}
	snapshot, drain := s.snapshot, s.drain
	locked = false
	s.mu.Unlock()
	// Runtime locks never nest under the transport mutex: legacy phase signals
	// and output need that mutex while finishing an in-flight operation.
	s.inventory.Lock()
	var finish func(bool)
	if p.Operation == "drain" && drain != nil {
		finish, err = drain()
		if err != nil {
			s.inventory.Unlock()
			return refusal("drain_runtime", err)
		}
	}
	committed := false
	defer func() {
		if finish != nil {
			if locked {
				s.mu.Unlock()
				locked = false
			}
			finish(committed)
		}
	}()
	candidates := snapshot()
	if p.Operation == "drain" || p.Operation == "update" {
		candidates = CustodyCandidates(candidates)
	}
	s.inventory.Unlock()
	s.mu.Lock()
	locked = true
	if s.draining {
		return refusal("keeper_draining", ErrRefused)
	}
	if err = s.acceptEpoch(p); err != nil {
		return err
	}
	if !validCandidates(candidates) {
		return refusal("inventory_bounds", ErrRefused)
	}
	if len(candidates) == 0 {
		candidates = nil // canonical empty representation, including omitted wire field
	}
	if p.Operation == "drain" {
		if len(candidates) != 0 {
			return refusal("drain_live_custody", ErrRefused)
		}
		// The operation and replacement build are covered by both signatures
		// and the stream key. No subsequent request can admit a writer.
		s.draining = true
		committed = true
		defer s.stop() // exit cleanly even if the authenticated client loses its ACK
	}
	i := inventory{Proof: p, Candidates: candidates}
	if err = w.write(message{Proof: p, Candidates: candidates, Signature: sign(s.identity.Keeper, "inventory", i), MAC: inventoryMAC(key, i)}); err != nil {
		return refusal("inventory_write", err)
	}
	if p.Operation == "service" {
		if s.service == nil {
			return refusal("service_unavailable", ErrRefused)
		}
		if s.active != nil {
			_ = s.active.Close()
		}
		s.active = conn
		channel, err := authenticatedChannel(conn, key, "client", "keeper", func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			durable, err := loadEpochWithKey(s.dir, s.identity.DaemonPublic)
			return err == nil && !s.draining && s.active == conn && s.epoch.Number == p.Epoch.Number && s.epoch.ID == p.Epoch.ID && durable.Number == p.Epoch.Number && durable.ID == p.Epoch.ID
		}, w.scan)
		if err != nil {
			return err
		}
		channel.generation = p.Generation
		locked = false
		s.mu.Unlock()
		stop := context.AfterFunc(s.ctx, func() { _ = conn.Close() })
		defer stop()
		return s.service(s.ctx, channel)
	}
	return nil
}

// Called under mu before and after runtime reconciliation. Wire claims never
// advance beyond local signed authority, including while a drain waits on I/O.
func (s *Server) acceptEpoch(p transcript) error {
	durable, err := loadEpochWithKey(s.dir, s.identity.DaemonPublic)
	if err != nil {
		return err
	}
	if !follows(durable, p.Epoch) || durable.Number != p.Epoch.Number || durable.ID != p.Epoch.ID || p.Epoch.Number < s.epoch.Number {
		return refusal("auth_epoch_order", ErrRefused)
	}
	s.epoch = p.Epoch
	return nil
}

func validCandidates(candidates []Candidate) bool {
	if len(candidates) > maxCandidates {
		return false
	}
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		if c.Allocation == "" || len(c.Allocation) > 128 || c.Custody == "" || len(c.Custody) > 128 || c.Shell == "" || len(c.Shell) > 128 || seen[c.Shell] {
			return false
		}
		seen[c.Shell] = true
	}
	return true
}

// Inspect performs both proofs. A successful inventory is still only a
// candidate list; callers must fence every allocation before any collection.
func Inspect(ctx context.Context, dir string, epoch Epoch) ([]Candidate, error) {
	view, err := inspectRetry(ctx, dir, epoch)
	return view.Candidates, err
}

type Snapshot struct {
	Candidates []Candidate
	Build      string
	Protocol   int
	Generation string
}

func inspectRetry(ctx context.Context, dir string, epoch Epoch) (Snapshot, error) {
	return requestRetry(ctx, dir, epoch, "")
}

func requestRetry(ctx context.Context, dir string, epoch Epoch, operation string) (Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		view, err := request(ctx, dir, epoch, operation)
		if !errors.Is(err, errBusy) {
			return view, err
		}
		if !waitRetry(ctx, 25*time.Millisecond) {
			return Snapshot{}, refusal("client_retry_exhausted", ctx.Err())
		}
	}
}

func inspect(ctx context.Context, dir string, epoch Epoch) (Snapshot, error) {
	return request(ctx, dir, epoch, "")
}

func request(ctx context.Context, dir string, epoch Epoch, operation string) (Snapshot, error) {
	return requestVersion(ctx, dir, epoch, operation, Version)
}

func requestVersion(ctx context.Context, dir string, epoch Epoch, operation string, custodyVersion int) (Snapshot, error) {
	return requestUsing(ctx, dir, epoch, operation, custodyVersion, "", nil)
}

func requestUsing(ctx context.Context, dir string, epoch Epoch, operation string, custodyVersion int, generation string, use func(*Channel, Snapshot) error) (Snapshot, error) {
	i, err := loadDaemon(dir)
	if err != nil {
		return Snapshot{}, err
	}
	if !validEpoch(publicKey(i.Daemon), epoch) {
		return Snapshot{}, refusal("client_epoch", ErrRefused)
	}
	path := filepath.Join(dir, "keeper.sock")
	if err = socketIdentity(path); err != nil {
		if os.IsNotExist(err) {
			_ = refusal("client_socket_starting", err)
			return Snapshot{}, errBusy
		}
		return Snapshot{}, refusal("client_socket", err)
	}
	dial := net.Dialer{}
	connection, err := dial.DialContext(ctx, "unix", path)
	if err != nil {
		if errors.Is(err, unix.ECONNREFUSED) || errors.Is(err, unix.EAGAIN) {
			_ = refusal("client_connect_starting", err)
			return Snapshot{}, errBusy
		}
		return Snapshot{}, refusal("client_connect", err)
	}
	defer func() { _ = connection.Close() }()
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		return Snapshot{}, refusal("client_transport", ErrRefused)
	}
	if err = peerIdentity(conn); err != nil {
		return Snapshot{}, refusal("client_uid", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	if use != nil {
		deadline = time.Now().Add(2 * time.Second)
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return Snapshot{}, refusal("client_deadline", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	n, err := nonce()
	if err != nil {
		return Snapshot{}, refusal("client_nonce", err)
	}
	secret, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return Snapshot{}, refusal("client_ephemeral", err)
	}
	build, err := BuildID()
	if err != nil {
		return Snapshot{}, refusal("client_build", err)
	}
	w := newWire(conn)
	version := custodyVersion
	if operation == "drain" || operation == "update" {
		version = maintenanceVersion
	}
	p := transcript{Operation: operation, Version: version, ClientNonce: n, Epoch: epoch, ClientPublic: secret.PublicKey().Bytes(), ClientBuild: build}
	if err = w.write(message{Proof: p}); err != nil {
		_ = refusal("client_hello", err)
		return Snapshot{}, errBusy
	}
	hello, err := w.read()
	if err != nil {
		_ = refusal("client_challenge", err)
		return Snapshot{}, errBusy
	}
	p.ServerNonce, p.Generation = hello.Proof.ServerNonce, hello.Proof.Generation
	p.KeeperPublic, p.KeeperBuild = hello.Proof.KeeperPublic, hello.Proof.KeeperBuild
	if len(p.ServerNonce) != 64 || len(p.Generation) != 64 || len(p.KeeperBuild) != 64 || len(hello.Candidates) != 0 || len(hello.MAC) != 0 || !reflect.DeepEqual(p, hello.Proof) || !verify(i.KeeperPublic, "keeper", p, hello.Signature) {
		return Snapshot{}, refusal("client_keeper_proof", ErrRefused)
	}
	if generation != "" && generation != p.Generation {
		return Snapshot{}, refusal("client_generation_changed", ErrRefused)
	}
	key, err := sessionKey(secret, p.KeeperPublic, p)
	if err != nil {
		return Snapshot{}, refusal("client_session_key", err)
	}
	view, err := finishClient(w, p, i, key)
	if err != nil || use == nil {
		return view, err
	}
	channel, err := authenticatedChannel(conn, key, "keeper", "client", nil, w.scan)
	if err != nil {
		return Snapshot{}, err
	}
	channel.generation = p.Generation
	return view, use(channel, view)
}

func finishClient(w *wire, p transcript, i Identity, key []byte) (Snapshot, error) {
	if err := w.write(message{Proof: p, Signature: sign(i.Daemon, "daemon", p)}); err != nil {
		_ = refusal("client_auth_write", err)
		return Snapshot{}, errBusy
	}
	result, err := w.read()
	if err != nil {
		_ = refusal("client_inventory_read", err)
		if errors.Is(err, io.ErrUnexpectedEOF) || isTransportError(err) {
			return Snapshot{}, errBusy
		}
		return Snapshot{}, ErrRefused
	}
	inv := inventory{Proof: p, Candidates: result.Candidates}
	if !reflect.DeepEqual(result.Proof, p) || !validCandidates(result.Candidates) || !verify(i.KeeperPublic, "inventory", inv, result.Signature) || !hmac.Equal(result.MAC, inventoryMAC(key, inv)) {
		return Snapshot{}, refusal("client_inventory_proof", ErrRefused)
	}
	return Snapshot{Candidates: result.Candidates, Build: p.KeeperBuild, Protocol: p.Version, Generation: p.Generation}, nil
}

func isTransportError(err error) bool {
	var transport *net.OpError
	return errors.As(err, &transport) || errors.Is(err, net.ErrClosed)
}

// CheckUpdate permits an absent keeper, retains a matching live keeper, or
// signs a drain of an empty different build and waits for singleton release.
// It never terminates a keeper holding candidates.
func CheckUpdate(ctx context.Context, dir string) error {
	epoch, err := loadEpoch(dir)
	if err != nil {
		return err
	}
	absent, err := keeperAbsent(dir)
	if err != nil || absent {
		return err
	}
	view, err := requestRetry(ctx, dir, epoch, "update")
	if err != nil {
		return err
	}
	build, err := BuildID()
	if err != nil {
		return refusal("update_build", err)
	}
	if len(view.Candidates) != 0 {
		if build != view.Build {
			if _, err := inspectRetry(ctx, dir, epoch); err != nil {
				_ = refusal("keeper_restart_required", err)
				return ErrRestartRequired
			}
		}
		return nil
	}
	if build == view.Build {
		return nil // Live old custody stays on its executable; installation never waits.
	}
	_, err = requestRetry(ctx, dir, epoch, "drain")
	return finishDrain(ctx, err, func() (bool, error) { return keeperAbsent(dir) }, waitRetry)
}

func finishDrain(ctx context.Context, err error, absentProbe func() (bool, error), pause func(context.Context, time.Duration) bool) error {
	if err != nil {
		// A lost acknowledgment is safe only after the singleton really exits.
		if absent, probeErr := absentProbe(); probeErr != nil || !absent {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		absent, err := absentProbe()
		if err != nil || absent {
			return err
		}
		if !pause(ctx, 10*time.Millisecond) {
			return refusal("drain_release_timeout", ctx.Err())
		}
	}
}

// A successful flock, including the post-lock inode proof, is the absence
// proof. Corrupt permissions, replaced files and other errors are refusals.
func keeperAbsent(dir string) (bool, error) {
	f, err := tryKeeperLock(dir, nil)
	if f != nil {
		_ = f.Close()
		return true, err
	}
	return false, err
}

func tryKeeperLock(dir string, after func(*os.File)) (*os.File, error) {
	if err := privateDir(dir); err != nil {
		return nil, refusal("update_directory", err)
	}
	f, err := privateFile(filepath.Join(dir, "keeper.lock"), unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, refusal("update_lock_open", err)
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, nil
		}
		return nil, refusal("update_lock", err)
	}
	if after != nil {
		after(f)
	}
	if err = sameLock(f, filepath.Join(dir, "keeper.lock")); err != nil {
		_ = f.Close()
		return nil, refusal("update_lock_replaced", err)
	}
	return f, nil
}

// Drain is the working operator remedy, including a matching-build keeper.
func Drain(ctx context.Context, dir string) error {
	epoch, err := loadEpoch(dir)
	if err != nil {
		return err
	}
	absent, err := keeperAbsent(dir)
	if err != nil || absent {
		return err
	}
	_, err = requestRetry(ctx, dir, epoch, "drain")
	return finishDrain(ctx, err, func() (bool, error) { return keeperAbsent(dir) }, waitRetry)
}

// RequireEmpty is the slice 2 reconciliation fence: no candidate can yet be
// adopted, so even Closing candidates block daemon startup (and therefore all
// collection, allocation replacement and writer admission). A failed request
// never becomes an empty inventory. Slice 3 may replace this only with proven
// adoption or completed physical cleanup, not a label or socket disconnect.
func RequireEmpty(ctx context.Context, dir string, epoch Epoch) error {
	view, err := inspectRetry(ctx, dir, epoch)
	if err != nil {
		return err
	}
	if len(view.Candidates) != 0 {
		return refusal("unreconciled_custody", ErrRefused)
	}
	return nil
}
