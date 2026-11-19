package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const hostLoginRefreshTimeout = 20 * time.Second
const hostLoginRefreshBefore = 2 * time.Minute
const hostLoginRefreshRetry = time.Minute

var errHostLoginRefresh = errors.New("host sign-in refresh unavailable")

// Only these daemon-owned codes may reach logs. Native errors and output are secret-bearing.
type hostLoginRefreshFailure string

func (e hostLoginRefreshFailure) Error() string { return "host sign-in refresh: " + string(e) }

func hostLoginRefreshCause(err error) string {
	var failure hostLoginRefreshFailure
	if errors.As(err, &failure) {
		switch failure {
		case "source", "temporary_directory", "stdin", "stdout", "start", "write", "protocol", "read", "account":
			return string(failure)
		}
	}
	return "unavailable"
}

var hostLoginRefreshState = struct {
	sync.Mutex
	tried map[string]time.Time
}{tried: map[string]time.Time{}}

// Called only by the background sign-in refresh loop. Registered sandbox directory names determine demand;
// no file the sandbox wrote is read, and no value from it reaches the native refresh process.
func refreshNeededSandboxHostLogins(now time.Time, home, codexSource string) {
	needed := map[string]bool{}
	sandboxRegistry.Range(func(k, _ any) bool {
		id, _ := k.(string)
		root, ok := sandboxHomePath(id)
		if !ok {
			return true
		}
		for _, engine := range []string{"claude", "codex"} {
			if st, err := os.Lstat(filepath.Join(root, engine)); err == nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
				needed[engine] = true
			}
		}
		return true
	})
	for _, login := range []struct {
		engine, source, file string
		read                 func(string) ([]byte, time.Time)
	}{
		{"claude", filepath.Join(home, ".claude"), claudeCredentialsN, claudeSandboxLogin},
		{"codex", codexSource, codexAuthN, codexSandboxLogin},
	} {
		spec := engineRegistry[login.engine]
		if !needed[login.engine] || spec.RefreshHostLogin == nil || login.source == "" {
			continue
		}
		// Serialize refreshes across all sessions. Re-read the host under this lock so an earlier
		// successful native rotation is observed before considering another request.
		hostLoginRefreshState.Lock()
		key := login.engine + ":" + login.source
		_, expiry := login.read(filepath.Join(login.source, login.file))
		if !expiry.IsZero() && !now.Add(hostLoginRefreshBefore).Before(expiry) && now.Sub(hostLoginRefreshState.tried[key]) >= hostLoginRefreshRetry {
			hostLoginRefreshState.tried[key] = now
			ctx, cancel := context.WithTimeout(context.Background(), hostLoginRefreshTimeout)
			// Never log native output, errors, or paths. Retry backoff also bounds diagnostics.
			// The copy loop verifies expiry and retains its manual sign-in notice on failure.
			if err := spec.RefreshHostLogin(ctx, login.source); err != nil {
				cause := hostLoginRefreshCause(err)
				if ctx.Err() != nil {
					cause = "timeout"
				}
				log.Printf("host_login_refresh engine=%s cause=%s", login.engine, cause)
			}
			cancel()
		}
		hostLoginRefreshState.Unlock()
	}
}

// Codex documents account/read {refreshToken:true} for its managed ChatGPT login. A fresh
// auth-only app-server runs from an empty host directory: no sandbox, thread, prompt, inference,
// tools, or project configuration. Codex owns refresh-token rotation and persistence on the host.
func refreshCodexHostLogin(parent context.Context, source string) error {
	if !filepath.IsAbs(source) {
		return hostLoginRefreshFailure("source")
	}
	ctx, cancel := context.WithTimeout(parent, hostLoginRefreshTimeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "blerg-login-refresh-")
	if err != nil {
		return hostLoginRefreshFailure("temporary_directory")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := resolveEngineBinary("codex", false)
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", "stdio://") //nolint:gosec // fixed native auth-only argv; daemon-resolved installed binary, no shell, bounded context
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + homeDir(), "CODEX_HOME=" + source}
	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "all_proxy", "no_proxy", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if v, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		return hostLoginRefreshFailure("stdin")
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return hostLoginRefreshFailure("stdout")
	}
	if err := cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return hostLoginRefreshFailure("start")
	}
	defer func() { _ = in.Close(); _ = cmd.Cancel(); _ = cmd.Wait() }()
	scan := bufio.NewScanner(io.LimitReader(out, 1<<20))
	scan.Buffer(make([]byte, 4096), 256<<10)
	send := json.NewEncoder(in)
	call := func(id int, method string, params any) (json.RawMessage, error) {
		if send.Encode(map[string]any{"id": id, "method": method, "params": params}) != nil {
			return nil, hostLoginRefreshFailure("write")
		}
		for scan.Scan() {
			var response struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scan.Bytes(), &response) != nil {
				continue
			}
			if response.Method != "" {
				continue
			} // no native request is ever authorized by this client
			if response.ID != id {
				continue
			}
			if len(response.Error) > 0 && string(response.Error) != "null" {
				return nil, hostLoginRefreshFailure("protocol")
			}
			return response.Result, nil
		}
		return nil, hostLoginRefreshFailure("read")
	}
	if _, err := call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "blerg_login_refresh", "version": "1"}}); err != nil {
		return err
	}
	if send.Encode(map[string]string{"method": "initialized"}) != nil {
		return hostLoginRefreshFailure("write")
	}
	raw, err := call(2, "account/read", map[string]bool{"refreshToken": true})
	if err != nil {
		return err
	}
	var result struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Account == nil || result.Account.Type != "chatgpt" {
		return hostLoginRefreshFailure("account")
	}
	return nil
}
