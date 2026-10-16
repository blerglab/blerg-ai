package daemon

// Input receipts bridge reconnect redelivery and engine consumption. The server owns the
// durable queue; this private journal prevents a replay from executing an accepted input twice.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

const inputReceiptLimit = 256

type inputReceipt struct {
	ID      string          `json:"id"`
	Hash    string          `json:"hash"`
	Acked   bool            `json:"acked,omitempty"`
	EventID string          `json:"event_id,omitempty"`
	Kind    string          `json:"kind,omitempty"`
	Ts      time.Time       `json:"ts,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}
type inputJournalState struct {
	SessionID string         `json:"session_id"`
	Receipts  []inputReceipt `json:"receipts"`
}
type inputJournal struct {
	mu     sync.Mutex
	dir    string
	state  inputJournalState
	queued map[string]bool
	closed bool
}

func newInputJournal(dir, sessionID string) (*inputJournal, error) {
	if sessionID == "" || safeFileName(sessionID) != sessionID {
		return nil, errors.New("unsafe input journal session id")
	}
	j := &inputJournal{dir: dir, state: inputJournalState{SessionID: sessionID}, queued: make(map[string]bool)}
	if dir == "" {
		return j, nil
	}
	if _, err := secureAgentRecordsDir(dir, true); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, sessionID+".json")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !ownedByDaemon(info) || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("input journal is not a private regular file")
	}
	b, err := os.ReadFile(path) //nolint:gosec // validated private regular file in the daemon-owned directory
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &j.state); err != nil {
		return nil, errors.New("invalid input journal")
	}
	if j.state.SessionID != sessionID || len(j.state.Receipts) > inputReceiptLimit {
		return nil, errors.New("invalid input journal identity or size")
	}
	return j, nil
}
func (j *inputJournal) save() error {
	if j.dir == "" || j.closed {
		return nil
	}
	b, err := json.Marshal(j.state)
	if err != nil {
		return err
	}
	path := filepath.Join(j.dir, j.state.SessionID+".json")
	tmp := path + "." + ccUUID() + ".tmp"
	if err = writeFileSynced(tmp, b); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp) }()
	return os.Rename(tmp, path)
}
func receiptEvent(r inputReceipt) (*agent.Event, error) {
	var payload any
	switch r.Kind {
	case "user_message":
		var p agent.UserMessagePayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return nil, err
		}
		payload = p
	case "error":
		var p agent.ErrorPayload
		if err := json.Unmarshal(r.Payload, &p); err != nil {
			return nil, err
		}
		payload = p
	default:
		return nil, errors.New("invalid input receipt event")
	}
	return &agent.Event{ClientEventID: r.EventID, Ts: r.Ts, Kind: r.Kind, Payload: payload}, nil
}
func (j *inputJournal) accept(in agent.Input) (bool, *agent.Event, error) {
	if j == nil || in.ClientID == "" {
		return true, nil, nil
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return false, nil, errors.New("session is stopping")
	}
	for _, r := range j.state.Receipts {
		if r.ID != in.ClientID {
			continue
		}
		if r.Hash != digest {
			return false, nil, errors.New("message id was already used for another input")
		}
		if r.EventID != "" {
			ev, err := receiptEvent(r)
			return false, ev, err
		}
		if j.queued[in.ClientID] {
			return false, nil, nil
		}
		j.queued[in.ClientID] = true
		return true, nil, nil
	}
	old := j.state.Receipts
	if len(old) >= inputReceiptLimit {
		at := -1
		for i, r := range old {
			if r.EventID != "" && r.Acked {
				at = i
				break
			}
		}
		if at < 0 {
			return false, nil, errors.New("too many pending messages")
		}
		j.state.Receipts = append(append([]inputReceipt(nil), old[:at]...), old[at+1:]...)
	}
	j.state.Receipts = append(append([]inputReceipt(nil), j.state.Receipts...), inputReceipt{ID: in.ClientID, Hash: digest})
	if err := j.save(); err != nil {
		j.state.Receipts = old
		return false, nil, fmt.Errorf("saving message receipt: %w", err)
	}
	j.queued[in.ClientID] = true
	return true, nil, nil
}
func inputEventClientID(ev agent.Event) string {
	switch p := ev.Payload.(type) {
	case agent.UserMessagePayload:
		if ev.Kind == "user_message" {
			return p.ClientID
		}
	case agent.ErrorPayload:
		if ev.Kind == "error" {
			return p.ClientID
		}
	}
	return ""
}
func (j *inputJournal) settle(ev agent.Event) error {
	id := inputEventClientID(ev)
	if j == nil || id == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	for i, r := range j.state.Receipts {
		if r.ID != id || r.EventID != "" {
			continue
		}
		payload, err := json.Marshal(ev.Payload)
		if err != nil {
			return err
		}
		old := j.state.Receipts[i]
		r.EventID, r.Ts, r.Kind, r.Payload = ev.ClientEventID, ev.Ts, ev.Kind, payload
		j.state.Receipts[i] = r
		if err := j.save(); err != nil {
			j.state.Receipts[i] = old
			return err
		}
		delete(j.queued, id)
		return nil
	}
	return nil
}
func (j *inputJournal) close() {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
	if j.dir != "" {
		_ = os.Remove(filepath.Join(j.dir, j.state.SessionID+".json"))
	}
}

func (j *inputJournal) ack(eventID string) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	for i, r := range j.state.Receipts {
		if r.EventID == eventID && !r.Acked {
			j.state.Receipts[i].Acked = true
			if err := j.save(); err != nil {
				j.state.Receipts[i].Acked = false
				return err
			}
			return nil
		}
	}
	return nil
}
