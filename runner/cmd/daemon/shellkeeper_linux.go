//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"golang.org/x/sys/unix"
)

func shellKeeperCommand(args []string) bool {
	if len(args) == 2 && args[0] == "shell-keeper" && args[1] == "--stdio" {
		os.Exit(shellkeeper.Stdio())
	}
	if len(args) == 0 || args[0] != "shell-keeper" {
		return false
	}
	if len(args) == 3 && args[1] == "install" {
		dir := filepath.Join(daemon.DaemonStateDir(), "shell-keeper")
		if shellkeeper.Install(context.Background(), dir, args[2]) != nil {
			os.Exit(1)
		}
		return true
	}
	if len(args) != 2 {
		keeperCommandRefused()
		return true
	}
	dir := filepath.Join(daemon.DaemonStateDir(), "shell-keeper")
	switch args[1] {
	case "init":
		if err := shellkeeper.Initialize(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	case "check-update":
		if err := shellkeeper.CheckUpdate(context.Background(), dir); err != nil {
			fmt.Fprintln(os.Stderr, "Host shells need reconnecting or restarting: keeper maintenance is unavailable; daemon installation and agents continue.")
		}
		return true
	case "drain":
		if shellkeeper.Drain(context.Background(), dir) != nil {
			os.Exit(1)
		}
		return true
	case "serve":
		os.Exit(serveKeeper(dir))
	}
	keeperCommandRefused()
	return true
}

func keeperCommandRefused() {
	failurelog.Default.Event("shell_keeper.command", slog.LevelError, "component", "shell_keeper", "stage", "command", "cause", "unsupported_command")
	os.Exit(1)
}

var keeperListen = listenWithShellService
var keeperConnect = shellruntime.ConnectKeeper
var keeperInspect = shellkeeper.InspectSnapshot
var keeperMaintenance = shellkeeper.CheckUpdate

func dialKeeper(life context.Context, dir string, epoch shellkeeper.Epoch) (shellruntime.HostBridge, error) {
	check, stop := context.WithTimeout(life, 2*time.Second)
	defer stop()
	view, err := keeperInspect(check, dir, epoch)
	if err != nil {
		// Only authenticated maintenance can distinguish incompatibility from
		// a short reconnect. Its existing frozen frame never authorizes custody.
		probe, cancel := context.WithTimeout(life, 2*time.Second)
		defer cancel()
		if errors.Is(keeperMaintenance(probe, dir), shellkeeper.ErrRestartRequired) {
			return nil, shellruntime.ErrKeeperIncompatible
		}
		return nil, err
	}
	if view.Protocol != shellkeeper.Version {
		failurelog.Default.Event("shell_keeper.startup_protocol", slog.LevelError, "cause", "protocol_or_generation_mismatch")
		return nil, shellruntime.ErrKeeperIncompatible
	}
	if len(view.Generation) != 64 {
		failurelog.Default.Event("shell_keeper.startup_generation", slog.LevelError, "cause", "generation_mismatch")
		return nil, shellkeeper.ErrRefused
	}
	bridge, err := keeperConnect(life, dir, epoch, view.Generation)
	if err != nil {
		return nil, err
	}
	return bridge, nil
}

func listenWithShellService(ctx context.Context, dir string) (*shellkeeper.Server, error) {
	broker := shellruntime.NewKeeperRuntime()
	server, err := shellkeeper.ListenWithRuntime(ctx, dir, shellkeeper.RuntimeHooks{Inventory: broker.Inventory, Serve: broker.Serve, Prepare: shellruntime.PrepareKeeperRuntime, Retire: broker.Shutdown, Drain: broker.BeginDrain})
	if err != nil {
		return nil, err
	}
	return server, nil
}

func serveKeeper(dir string) int {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		failurelog.Default.Event("shell_keeper.protect", slog.LevelError, "component", "shell_keeper", "cause", failurelog.Cause(err))
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server, err := keeperListen(ctx, dir)
	if err != nil {
		if errors.Is(err, shellkeeper.ErrUnsupported) {
			return 78
		}
		return 1
	}
	if shellkeeper.NotifyReady(ctx, os.Getenv("NOTIFY_SOCKET")) != nil {
		_ = server.Close()
		return 1
	}
	select {
	case <-ctx.Done():
	case <-server.Done():
	}
	if server.Close() != nil || (ctx.Err() == nil && !server.Drained()) {
		return 1
	}
	return 0
}

// Keeper authority fences shells only. Missing state, a failed keeper, and
// reconnects must never prevent agent startup or own the daemon lifetime.
func keeperStartup() (func(), error) {
	dir := filepath.Join(daemon.DaemonStateDir(), "shell-keeper")
	ctx, cancel := context.WithCancel(context.Background())
	maintenanceDone := make(chan struct{})
	go func() {
		defer close(maintenanceDone)
		delay := 5 * time.Second
		for {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			call, stop := context.WithTimeout(ctx, 2*time.Second)
			err := shellkeeper.CheckUpdate(call, dir)
			stop()
			if err != nil {
				delay = min(time.Minute, delay*2)
			} else {
				delay = 5 * time.Second
			}
		}
	}()
	var once sync.Once
	var mu sync.Mutex
	closed := false
	var bridge *shellruntime.ReconnectingHostBridge
	var authority *shellkeeper.DaemonAuthority
	daemon.SetHostKeeperFactory(func() shellruntime.HostBridge {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return shellruntime.RefusedHostBridge()
		}
		once.Do(func() {
			bridge = shellruntime.NewReconnectingHostBridge(ctx, func(life context.Context) (shellruntime.HostBridge, error) {
				var err error
				if authority == nil {
					authority, err = shellkeeper.AcquireDaemon(dir)
					if err != nil {
						return nil, err
					}
				}
				return dialKeeper(life, dir, authority.Epoch)
			})
		})
		return bridge
	})
	return func() {
		cancel()
		<-maintenanceDone
		mu.Lock()
		closed = true
		connected := bridge
		mu.Unlock()
		if connected != nil {
			_ = connected.Close()
		}
		daemon.SetHostKeeperFactory(nil)
		if authority != nil {
			_ = authority.Close()
		}
	}, nil
}
