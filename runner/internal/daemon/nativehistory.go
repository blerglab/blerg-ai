package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/nativehistory"
)

var errNativeConversation = errors.New("the retained native conversation is missing or does not match this session; it was not replaced with a new conversation")

// NativeHistoryConfig is trusted pod setup, never a browser spawn field. It
// names only conversation storage; credentials and tool grants remain fresh.
type NativeHistoryConfig struct {
	Directory, AccountID, ClaimUID string
	Resume                         bool
}

type nativeConversation struct {
	ToolSchemaHash string         `json:"tool_schema_hash,omitempty"`
	Version        int            `json:"version"`
	SessionID      string         `json:"session_id"`
	AccountID      string         `json:"account_id"`
	ClaimUID       string         `json:"claim_uid"`
	Engine         string         `json:"engine"`
	WorkDir        string         `json:"work_dir"`
	NativeID       string         `json:"native_id,omitempty"`
	CurrentTurnID  string         `json:"current_turn_id,omitempty"`
	KnownSubagents []OpenSubagent `json:"known_subagents,omitempty"`
}

type nativeHistoryKeeper struct {
	mu     sync.Mutex
	root   *os.Root
	state  nativeConversation
	resume bool
}

func openNativeHistory(cfg NativeHistoryConfig, sessionID, engine, workDir string) (*nativeHistoryKeeper, error) {
	if _, ok := nativehistory.ForEngine(engine); !ok {
		return nil, errNativeConversation
	}
	info, err := os.Lstat(cfg.Directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByDaemon(info) || info.Mode().Perm()&0077 != 0 {
		return nil, errNativeConversation
	}
	root, err := os.OpenRoot(cfg.Directory)
	if err != nil {
		return nil, errNativeConversation
	}
	k := &nativeHistoryKeeper{root: root, resume: cfg.Resume, state: nativeConversation{Version: 1, SessionID: sessionID, Engine: models.NormalizeEngine(engine), AccountID: cfg.AccountID, ClaimUID: cfg.ClaimUID, WorkDir: workDir}}
	if cfg.AccountID == "" || cfg.ClaimUID == "" {
		_ = root.Close()
		return nil, errNativeConversation
	}
	if cfg.Resume {
		meta, err := root.Lstat("conversation.json")
		if err != nil || !meta.Mode().IsRegular() || meta.Mode()&os.ModeSymlink != 0 || meta.Mode().Perm()&0077 != 0 || !ownedByDaemon(meta) {
			_ = root.Close()
			return nil, errNativeConversation
		}
		f, err := root.Open("conversation.json")
		if err != nil {
			_ = root.Close()
			return nil, errNativeConversation
		}
		raw, readErr := io.ReadAll(io.LimitReader(f, 1<<20+1))
		_ = f.Close()
		var stored nativeConversation
		if readErr != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &stored) != nil || stored.Version != 1 || stored.SessionID != sessionID || stored.Engine != k.state.Engine || stored.AccountID != cfg.AccountID || stored.ClaimUID != cfg.ClaimUID || stored.WorkDir != workDir || !validCodexThreadID(stored.NativeID) || len(stored.KnownSubagents) > codexMaxKnownChildren {
			_ = root.Close()
			return nil, errNativeConversation
		}
		k.state = stored
	} else {
		if _, err := root.Lstat("conversation.json"); !os.IsNotExist(err) {
			_ = root.Close()
			return nil, errNativeConversation
		}
		if err = k.save(); err != nil {
			_ = root.Close()
			return nil, err
		}
	}
	return k, nil
}

func (k *nativeHistoryKeeper) save() error {
	raw, err := json.Marshal(k.state)
	if err != nil || len(raw) > 1<<20 {
		return errNativeConversation
	}
	name := "conversation-" + ccUUID() + ".tmp"
	f, err := k.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errNativeConversation
	}
	defer func() { _ = k.root.Remove(name) }()
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil || k.root.Rename(name, "conversation.json") != nil {
		return errNativeConversation
	}
	dir, err := k.root.Open(".")
	if err != nil {
		return errNativeConversation
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return errNativeConversation
	}
	return nil
}

func (k *nativeHistoryKeeper) setNativeID(id string) error {
	if !validCodexThreadID(id) {
		return errNativeConversation
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.state.NativeID != "" && k.state.NativeID != id {
		return errNativeConversation
	}
	k.state.NativeID = id
	return k.save()
}

func (k *nativeHistoryKeeper) observe(ev agent.Event) error {
	if k == nil {
		return nil
	}
	p, ok := ev.Payload.(agent.UserMessagePayload)
	if ev.Kind != "user_message" || !ok {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.state.CurrentTurnID = p.TurnID
	return k.save()
}

func (k *nativeHistoryKeeper) bind(driver sessionDriver, home string, fault func()) error {
	switch d := driver.(type) {
	case *codexDriver:
		d.NativeStateHome = filepath.Join(filepath.Dir(k.root.Name()), "sqlite")
		if d.RuntimeConfig == nil {
			d.RuntimeConfig = map[string]any{}
		}
		d.RuntimeConfig["sqlite_home"] = filepath.Join(filepath.Dir(k.root.Name()), "sqlite")
		if r := d.RestrictedRuntime; r != nil {
			digest, err := nativeToolSchemaHash(r.Specs())
			if err != nil {
				return errNativeConversation
			}
			k.mu.Lock()
			if k.resume {
				r.nativeSchemaChanged = k.state.ToolSchemaHash == "" || k.state.ToolSchemaHash != digest
			} else {
				k.state.ToolSchemaHash = digest
				err = k.save()
			}
			k.mu.Unlock()
			if err != nil {
				return errNativeConversation
			}

			parent := filepath.Join(home, ".blerg-native-runtime")
			if err := os.Mkdir(parent, 0700); err != nil && !os.IsExist(err) {
				return errNativeConversation
			}
			info, err := os.Lstat(parent)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByDaemon(info) || info.Mode().Perm()&0077 != 0 {
				return errNativeConversation
			}
			dest := filepath.Join(parent, "codex")
			if _, err = os.Lstat(dest); !os.IsNotExist(err) {
				return errNativeConversation
			}
			source, err := os.Lstat(r.Home) //nolint:gosec // This path is returned only by daemon runtime preparation, never by native history or a spawn field.
			if err != nil || !source.IsDir() || source.Mode()&os.ModeSymlink != 0 || !ownedByDaemon(source) || source.Mode().Perm()&0077 != 0 {
				return errNativeConversation
			}
			if err = os.Rename(r.Home, dest); err != nil { //nolint:gosec // Both paths are owned private directories, the source is fresh engine setup and destination is fixed outside the workspace.

				return errNativeConversation
			}
			r.Home, d.CodexHome = dest, dest
			sessions := filepath.Join(dest, "sessions")
			if _, err = os.Lstat(sessions); os.IsNotExist(err) {
				if err = os.Symlink(filepath.Join(home, ".codex", "sessions"), sessions); err != nil {
					return errNativeConversation
				}
			} else if err != nil {
				return errNativeConversation
			}
		}
		prior := d.onThreadID
		d.onThreadID = func(id string) {
			if k.setNativeID(id) != nil {
				fault()
				return
			}
			if prior != nil {
				prior(id)
			}
		}
		known := d.onKnownChildren
		d.onKnownChildren = func(children []OpenSubagent) {
			k.mu.Lock()
			k.state.KnownSubagents = append([]OpenSubagent(nil), children...)
			err := k.save()
			k.mu.Unlock()
			if err != nil {
				fault()
				return
			}
			if known != nil {
				known(children)
			}
		}
		if k.resume {
			d.threadID = k.state.NativeID
			d.usageTurnID = k.state.CurrentTurnID
			d.seedKnownChildren(k.state.KnownSubagents)
			d.adoptionNote = "[system] The native conversation was retained across pod replacement. The workspace was reconstructed separately. Verify its current state before repeating any earlier action; no previous command was automatically replayed."
		}
	case *claudeCodeDriver:
		d.requireNativeResume = k.resume
		prior := d.onSessionID
		d.onSessionID = func(id string) {
			if k.setNativeID(id) != nil {
				fault()
				return
			}
			if prior != nil {
				prior(id)
			}
		}
		if k.resume {
			d.adopt(k.state.NativeID, adoption{hadConversation: true, cutOff: true, work: "The native conversation survived pod replacement. The workspace was reconstructed; verify it before repeating earlier actions."})
			d.usageTurnID = k.state.CurrentTurnID
		}
	default:
		return errNativeConversation
	}
	return nil
}

// Only model-visible tool definitions enter this digest. Routes, credentials,
// runtime config and native IDs are deliberately absent. Canonical JSON keeps
// equivalent object order/whitespace from producing a false change notice.
func nativeToolSchemaHash(specs []map[string]any) (string, error) {
	raw, err := json.Marshal(specs)
	if err != nil {
		return "", err
	}
	var canonical any
	if err = json.Unmarshal(raw, &canonical); err != nil {
		return "", err
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
